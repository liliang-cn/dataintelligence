package intake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/sqldialect"

	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Brain is the handle the plans are kept on. *cortexdb.DB satisfies it; the
// interface is here so this package asks for the two things it uses rather
// than for the whole brain.
type Brain interface {
	SQL() *sql.DB
	Dialect() sqldialect.Dialect
}

// Store is the intake plans and their ledger.
type Store struct {
	db      *sql.DB
	dialect sqldialect.Dialect
	now     func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock fixes time, so two acts in one test are distinguishable.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// Two tables on the brain's own handle, in the arrangement athanor's
// ontologies and livedb use: a package that needs storage takes SQL() and
// Dialect() and owns tables nobody else writes.
//
// Timestamps are TEXT in RFC 3339 rather than DATETIME / TIMESTAMPTZ. They are
// written by this package's clock and read back only by it, and one spelling
// that scans the same way on both backends is worth more than a native type
// nothing here queries by range.
var ddl = []string{
	`CREATE TABLE IF NOT EXISTS di_intake_plans (
		id         TEXT PRIMARY KEY,
		source     TEXT NOT NULL,
		driver     TEXT NOT NULL,
		treatments TEXT NOT NULL DEFAULT '[]',
		hash       TEXT NOT NULL,
		state      TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		signed_by  TEXT NOT NULL DEFAULT '',
		signed_at  TEXT NOT NULL DEFAULT '',
		supersedes TEXT NOT NULL DEFAULT '',
		note       TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS di_intake_plans_source ON di_intake_plans(source)`,
	// One plan in force per source, held by the table rather than only by the
	// transaction that intends it — livedb's signedIndex. Sign already
	// supersedes the previous plan, so this should never fire; if a second
	// path to signing is ever added and forgets, the second signed row is
	// refused rather than making Current a coin toss.
	`CREATE UNIQUE INDEX IF NOT EXISTS di_intake_one_signed
		ON di_intake_plans(source) WHERE state = 'signed'`,
	`CREATE TABLE IF NOT EXISTS di_intake_ledger (
		id        TEXT PRIMARY KEY,
		at        TEXT NOT NULL,
		act       TEXT NOT NULL,
		plan      TEXT NOT NULL,
		source    TEXT NOT NULL,
		by_whom   TEXT NOT NULL DEFAULT '',
		from_hash TEXT NOT NULL DEFAULT '',
		to_hash   TEXT NOT NULL DEFAULT '',
		note      TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS di_intake_ledger_source ON di_intake_ledger(source, at)`,
}

// New ensures the tables on the brain and returns the store.
func New(brain Brain, opts ...Option) (*Store, error) {
	if brain == nil || brain.SQL() == nil {
		return nil, errors.New("intake: no brain to keep the plans on")
	}
	s := &Store{db: brain.SQL(), dialect: brain.Dialect(),
		now: func() time.Time { return time.Now().UTC() }}
	if s.dialect == nil {
		s.dialect = sqldialect.For(sqldialect.SQLite)
	}
	for _, o := range opts {
		o(s)
	}
	for _, q := range ddl {
		if _, err := s.db.ExecContext(context.Background(), q); err != nil {
			return nil, fmt.Errorf("intake: create schema: %w", err)
		}
	}
	return s, nil
}

// Propose reads the warehouse's catalogue — no rows — and writes a draft.
//
// dsn names the database; its credential is removed before anything is
// stored (SourceOf), so passing the real DSN is the intended use.
func (s *Store) Propose(ctx context.Context, wh *warehouse.Warehouse, dsn, by, note string) (Plan, error) {
	schema, err := modelgen.Catalogue(ctx, wh)
	if err != nil {
		return Plan{}, fmt.Errorf("intake: read the catalogue: %w", err)
	}
	cols := treatmentsFor(schema)
	if len(cols) == 0 {
		return Plan{}, errors.New("intake: the database holds no table a plan could cover")
	}
	p := Plan{
		Source: SourceOf(dsn), Driver: wh.Driver(), Columns: cols, State: Draft,
		CreatedBy: strings.TrimSpace(by), CreatedAt: s.at(), Note: note,
	}
	if p.ID, err = mintID("intake"); err != nil {
		return Plan{}, err
	}
	p.Hash = planHash(p)
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		treatments, _ := json.Marshal(p.Columns)
		if _, err := s.exec(ctx, tx, `INSERT INTO di_intake_plans
			(id, source, driver, treatments, hash, state, created_by, created_at, note)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Source, p.Driver, string(treatments), p.Hash, string(p.State),
			p.CreatedBy, stamp(p.CreatedAt), p.Note); err != nil {
			return fmt.Errorf("intake: write draft: %w", err)
		}
		return s.record(ctx, tx, "propose", p, p.CreatedBy, "", note)
	})
	return p, err
}

// Amend applies a person's overrides to a draft and returns it with its new
// hash. by is required: an amendment is a decision, and the ledger line for a
// decision by nobody would be the kind of record this package exists to
// replace.
func (s *Store) Amend(ctx context.Context, id string, changes []Change, by string) (Plan, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return Plan{}, fmt.Errorf("intake: an amendment needs a name — who is changing plan %s", id)
	}
	var p Plan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if p, err = s.load(ctx, tx, id); err != nil {
			return err
		}
		if p.State != Draft {
			return fmt.Errorf("%w: %s is %s — propose a new plan", ErrNotDraft, id, p.State)
		}
		if err := verify(p); err != nil {
			return err
		}
		from := p.Hash
		if err := amend(&p, changes, by); err != nil {
			return err
		}
		if p.Hash == from {
			return nil
		}
		treatments, _ := json.Marshal(p.Columns)
		if _, err := s.exec(ctx, tx, `UPDATE di_intake_plans SET treatments = ?, hash = ? WHERE id = ?`,
			string(treatments), p.Hash, id); err != nil {
			return fmt.Errorf("intake: amend %s: %w", id, err)
		}
		return s.record(ctx, tx, "amend", p, by, from, describe(changes))
	})
	if err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Sign records that `by` approved the plan, having read the version whose
// hash is `hash`, and puts it in force for its source.
//
// It refuses rather than warns in three cases — rollout.Sign's three, for the
// same reason: each would make the signature say something untrue.
//
//   - no signer: an approval by nobody is not an approval
//   - the stored plan no longer hashes to its recorded hash: its treatments
//     were changed without Amend, and whatever the signer read, it was not
//     proven to be this
//   - the signer names a hash that is not the plan's current one (the full
//     hash, or at least its first twelve digits, which is what is printed):
//     they are approving something they did not read. An empty hash is the
//     same refusal — rollout.Sign lets that through, and a signature that
//     names nothing binds to nothing
//
// A plan that is not a draft is refused too, with ErrNotDraft.
func (s *Store) Sign(ctx context.Context, id, hash, by, note string) (Plan, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return Plan{}, fmt.Errorf("intake: a signature needs a signer — pass who is approving plan %s", id)
	}
	var p Plan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if p, err = s.load(ctx, tx, id); err != nil {
			return err
		}
		if p.State != Draft {
			return fmt.Errorf("%w: %s is %s", ErrNotDraft, id, p.State)
		}
		if err := verify(p); err != nil {
			return err
		}
		if h := strings.ToLower(strings.TrimSpace(hash)); len(h) < 12 || !strings.HasPrefix(p.Hash, h) {
			return fmt.Errorf("%w: you signed %q but plan %s is %s — "+
				"you would be approving a plan you have not read", ErrStaleHash, hash, id, p.Short())
		}

		var prev Plan
		prevErr := s.current(ctx, tx, p.Source, &prev)
		switch {
		case errors.Is(prevErr, ErrUnsigned):
		case prevErr != nil:
			return prevErr
		default:
			if _, err := s.exec(ctx, tx, `UPDATE di_intake_plans SET state = ? WHERE id = ?`,
				string(Superseded), prev.ID); err != nil {
				return fmt.Errorf("intake: supersede %s: %w", prev.ID, err)
			}
			prev.State = Superseded
			if err := s.record(ctx, tx, "supersede", prev, by, prev.Hash, "superseded by "+id); err != nil {
				return err
			}
			p.Supersedes = prev.ID
		}

		p.State, p.SignedBy, p.SignedAt = Signed, by, s.at()
		if note != "" {
			p.Note = note
		}
		if _, err := s.exec(ctx, tx, `UPDATE di_intake_plans
			SET state = ?, signed_by = ?, signed_at = ?, supersedes = ?, note = ? WHERE id = ?`,
			string(p.State), p.SignedBy, stamp(p.SignedAt), p.Supersedes, p.Note, id); err != nil {
			return fmt.Errorf("intake: sign %s: %w", id, err)
		}
		return s.record(ctx, tx, "sign", p, by, "", note)
	})
	if err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Get returns one plan.
func (s *Store) Get(ctx context.Context, id string) (Plan, error) { return s.load(ctx, nil, id) }

// Current returns the plan in force for a database, or ErrUnsigned.
func (s *Store) Current(ctx context.Context, dsn string) (Plan, error) {
	var p Plan
	err := s.current(ctx, nil, SourceOf(dsn), &p)
	return p, err
}

// List returns every plan for a database, newest first. An empty dsn lists
// every source.
func (s *Store) List(ctx context.Context, dsn string) ([]Plan, error) {
	q := `SELECT ` + planColumns + ` FROM di_intake_plans`
	var args []any
	if dsn != "" {
		q += ` WHERE source = ?`
		args = append(args, SourceOf(dsn))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("intake: list plans: %w", err)
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Ledger is every act on a database's plans, oldest first — the answer to
// "who agreed that DI could read this, and what exactly did they agree to".
// An empty dsn returns every source's.
func (s *Store) Ledger(ctx context.Context, dsn string) ([]Entry, error) {
	q := `SELECT at, act, plan, source, by_whom, from_hash, to_hash, note FROM di_intake_ledger`
	var args []any
	if dsn != "" {
		q += ` WHERE source = ?`
		args = append(args, SourceOf(dsn))
	}
	q += ` ORDER BY at, id`
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("intake: read the ledger: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var at string
		if err := rows.Scan(&at, &e.Act, &e.Plan, &e.Source, &e.By, &e.FromHash, &e.ToHash, &e.Note); err != nil {
			return nil, err
		}
		e.At = parseStamp(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Check compares the live catalogue against the plan in force, and reports.
// It refuses only when there is nothing to compare against — no signed plan,
// or one whose stored treatments no longer match its hash. Drift is in the
// Report; Require is the version that refuses on it.
func (s *Store) Check(ctx context.Context, wh *warehouse.Warehouse, dsn string) (Report, error) {
	source := SourceOf(dsn)
	var p Plan
	if err := s.current(ctx, nil, source, &p); err != nil {
		if errors.Is(err, ErrUnsigned) {
			return Report{}, s.unsigned(ctx, source)
		}
		return Report{}, err
	}
	if err := verify(p); err != nil {
		return Report{}, err
	}
	schema, err := modelgen.Catalogue(ctx, wh)
	if err != nil {
		return Report{}, fmt.Errorf("intake: read the catalogue: %w", err)
	}
	r := Report{Plan: p}
	r.Unclassified, r.Gone, r.Retyped = drift(p, schema)
	return r, nil
}

// Require is the gate: call it before a command reads the customer's rows.
//
// It returns the plan in force, whose Treatment says per column whether it
// may be read, and how. It refuses when no plan is signed for this database,
// and when the database now holds a column the signed plan does not classify
// — naming every such column, because a column added after the signature is
// the one column nobody decided about, and a new `id_card` column is exactly
// the thing a customer's DBA adds without telling anyone.
func (s *Store) Require(ctx context.Context, wh *warehouse.Warehouse, dsn string) (Plan, error) {
	r, err := s.Check(ctx, wh, dsn)
	if err != nil {
		return Plan{}, err
	}
	if len(r.Unclassified) > 0 {
		return Plan{}, fmt.Errorf("%w: %s appeared after plan %s was signed by %s — "+
			"nobody decided whether DI may read it; propose, amend and sign a new plan",
			ErrDrift, strings.Join(r.Unclassified, ", "), r.Plan.ID, r.Plan.SignedBy)
	}
	return r.Plan, nil
}

// unsigned is the refusal for a source with nothing in force, naming the
// draft waiting to be signed when there is one — the next step, not just the
// fact.
func (s *Store) unsigned(ctx context.Context, source string) error {
	q := `SELECT ` + planColumns + ` FROM di_intake_plans WHERE source = ? AND state = ?
		ORDER BY created_at DESC, id DESC`
	p, err := scanPlan(s.db.QueryRowContext(ctx, s.dialect.Rebind(q), source, string(Draft)))
	if err == nil {
		return fmt.Errorf("%w for %s: draft %s (hash %s) is waiting — read it, then sign that hash",
			ErrUnsigned, source, p.ID, p.Short())
	}
	return fmt.Errorf("%w for %s: propose one, have somebody at the customer read and sign it, "+
		"then run this again", ErrUnsigned, source)
}

// verify re-derives a stored plan's hash from its stored treatments. The two
// were written together by this package; if they disagree, the row was
// edited by something else, and neither signing it nor trusting its
// signature would mean anything.
func verify(p Plan) error {
	if got := planHash(p); got != p.Hash {
		return fmt.Errorf("%w: plan %s records %s but its treatments hash to %s",
			ErrTampered, p.ID, p.Short(), short(got))
	}
	return nil
}

const planColumns = `id, source, driver, treatments, hash, state,
	created_by, created_at, signed_by, signed_at, supersedes, note`

type rowScanner interface{ Scan(dest ...any) error }

func scanPlan(r rowScanner) (Plan, error) {
	var (
		p                   Plan
		treatments, state   string
		createdAt, signedAt string
	)
	if err := r.Scan(&p.ID, &p.Source, &p.Driver, &treatments, &p.Hash, &state,
		&p.CreatedBy, &createdAt, &p.SignedBy, &signedAt, &p.Supersedes, &p.Note); err != nil {
		return Plan{}, err
	}
	p.State = State(state)
	p.CreatedAt, p.SignedAt = parseStamp(createdAt), parseStamp(signedAt)
	if err := json.Unmarshal([]byte(treatments), &p.Columns); err != nil {
		return Plan{}, fmt.Errorf("intake: plan %s: reading its treatments back: %w", p.ID, err)
	}
	return p, nil
}

func (s *Store) load(ctx context.Context, tx *sql.Tx, id string) (Plan, error) {
	q := s.dialect.Rebind(`SELECT ` + planColumns + ` FROM di_intake_plans WHERE id = ?`)
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, q, id)
	} else {
		row = s.db.QueryRowContext(ctx, q, id)
	}
	p, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, fmt.Errorf("%w: %s", ErrNoPlan, id)
	}
	return p, err
}

func (s *Store) current(ctx context.Context, tx *sql.Tx, source string, into *Plan) error {
	q := s.dialect.Rebind(`SELECT ` + planColumns + ` FROM di_intake_plans
		WHERE source = ? AND state = ? ORDER BY signed_at DESC, id DESC`)
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, q, source, string(Signed))
	} else {
		row = s.db.QueryRowContext(ctx, q, source, string(Signed))
	}
	p, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w for %s", ErrUnsigned, source)
	}
	if err != nil {
		return err
	}
	*into = p
	return nil
}

// record appends one ledger line inside the act's own transaction: an act
// that committed without its line is the case this package exists to
// prevent, so the two commit together or not at all.
func (s *Store) record(ctx context.Context, tx *sql.Tx, act string, p Plan, by, from, note string) error {
	id, err := mintID("act")
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx, tx, `INSERT INTO di_intake_ledger
		(id, at, act, plan, source, by_whom, from_hash, to_hash, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, stamp(s.at()), act, p.ID, p.Source, by, from, p.Hash, note); err != nil {
		return fmt.Errorf("intake: write the %s of %s to the ledger: %w", act, p.ID, err)
	}
	return nil
}

func (s *Store) exec(ctx context.Context, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.dialect.Rebind(q), args...)
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("intake: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// describe renders an amendment for the ledger's note.
func describe(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, ch := range changes {
		what := ch.Table
		if ch.Column != "" {
			what += "." + ch.Column
		}
		line := what + "→" + string(ch.Action)
		if ch.Reason != "" {
			line += " (" + ch.Reason + ")"
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "; ")
}

// at is the clock, truncated to microseconds — livedb's reasoning: the value
// read back must equal the value returned, on every backend.
func (s *Store) at() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func parseStamp(v string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05.000000Z", v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func mintID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("intake: mint %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
