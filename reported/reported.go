// Package reported is what we said, kept.
//
// A general coding agent handed a customer's schema computes correctly — it
// was measured doing so, fan-out trap avoided on its own — and still produced
// a first-pass yield twelve times off the one the quality department had
// signed, because it picked a different denominator that was every bit as
// reasonable. Computing correctly is therefore not what this product is for.
// Recording is: which definition was approved, by whom, and what was actually
// sent out under it. brief and board cover the first two at the moment of
// asking. This package covers the third, and the question that only arrives a
// week later: what did we report on the 14th, and why is today's number
// different?
//
// No agent can answer that by recomputing. It can compute today; it cannot
// reconstruct what the organisation believed on the 14th, because the belief
// was a figure, a definition hash and a signature at that instant, and all
// three may have moved since. A record can. So Freeze writes the delivered
// figure down exactly as it left — rows, SQL, model hash, the approval that
// stood behind that hash at the time, the role it was computed as — and
// Compare reruns the same semantic query now and says, row by row, what
// moved and why.
//
// # Why the why is the whole package
//
// Two numbers that differ for different reasons look identical in a diff of
// rows. A figure that dropped because the quality department re-signed the
// denominator and a figure that dropped because last week's castings finally
// landed are two different conversations with two different people, and a
// report that prints both as "changed" has handed the reader the one job it
// exists to do. Compare attributes every movement to one of three causes —
// the definition, the access policy, or the data — and when more than one
// moved at once it separates them by replaying the old definition against
// today's warehouse. What moved between the frozen figure and the replay is
// the data; what moved between the replay and today is the definition. When
// the old definition was not frozen with the report the two cannot be
// separated, and the comparison says so rather than guessing.
//
// # Where it is kept, and why not in a snapshot
//
// athanor's pkg/snapshots was the first candidate, since the owner's direction
// is to use athanor rather than re-implement it. It does not fit, and not for
// a reason a patch would fix: a snapshot is a name, an instant and the graph's
// counts, and its schema note says there is no body column "and there never
// will be one", because the graph's bytes are already in the brain's history
// tables and a second copy would drift. A delivered figure is the opposite
// case. Its rows exist nowhere else — the warehouse is the customer's, is
// read-only to us, and will not answer for the 14th — so the copy is not a
// second opinion, it is the only record. Putting it in a snapshot would mean
// either a body column the package has deliberately refused or a graph node
// per row, which would make a table of figures answer to graph diffs.
//
// What this package does take from snapshots is its arrangement: one table on
// the brain's own handle (db.SQL() and db.Dialect(), the pattern
// pkg/ontologies and pkg/snapshots share), `?` placeholders rebound per
// dialect, and a primary key that is the name a person chose. The record
// lives in the same file as the corpus and the graph, so a backup of the
// brain carries what we told people alongside what we knew.
//
// # Once sent, never changed
//
// There is no UPDATE in this package. A report that went out is in someone's
// inbox, and rewriting our copy would leave them holding a figure we can no
// longer show we sent. A correction is a new report under a new name that
// says which one it supersedes and why; the original stays readable, and
// reading it names its successor. See Freezing.Supersedes.
package reported

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/sqldialect"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/brief"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/rollout"
)

var (
	// ErrExists is a name already used by a frozen report.
	ErrExists = errors.New("reported: already frozen")
	// ErrNotFound is a name nothing was frozen under.
	ErrNotFound = errors.New("reported: no such report")
	// ErrInvalid is a report that could not be argued from later.
	ErrInvalid = errors.New("reported: invalid")
	// ErrSuperseded is a second correction to a report that already has one.
	ErrSuperseded = errors.New("reported: already superseded")
)

// Approval is what the registry said about a model hash at one moment.
//
// Checked separates "nobody approved it" from "nobody asked": a store opened
// without a registry cannot say who signed anything, and printing that as
// NOBODY would accuse a definition of being unapproved when it was only
// unlooked-up.
type Approval struct {
	Checked  bool   `json:"checked"`
	Found    bool   `json:"found"`
	SignedBy string `json:"signed_by,omitempty"`
	SignedAt string `json:"signed_at,omitempty"`
	Note     string `json:"note,omitempty"`
	Promoted bool   `json:"promoted,omitempty"`
	Err      string `json:"err,omitempty"`
}

// Report is one delivered figure, as it left.
type Report struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	// By is who sent it. Note is what was said with it; for a correction it
	// is the reason for the correction.
	By   string `json:"by"`
	Note string `json:"note,omitempty"`

	// Supersedes is the report this one corrects. SupersededBy is filled on
	// read, from whichever report names this one — it is never written into
	// this row, because this row is never written twice.
	Supersedes   string `json:"supersedes,omitempty"`
	SupersededBy string `json:"-"`

	Question string               `json:"question,omitempty"`
	Query    semantic.Query       `json:"query"`
	Who      governance.Principal `json:"who"`

	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	SQL     string   `json:"sql"`

	// ModelHash names the definition. Model is the definition itself, when
	// the caller had it: with it, Compare can replay the old definition on
	// today's data and separate a data movement from a definition movement;
	// without it, a definition change hides whatever the data did.
	ModelHash string   `json:"model_hash"`
	Model     string   `json:"model,omitempty"`
	Approval  Approval `json:"approval"`

	// Policy is the access policy the figure was computed under, kept whole
	// so the replay can use it; PolicyHash is how Compare notices it moved.
	Policy     governance.Policy `json:"policy"`
	PolicyHash string            `json:"policy_hash"`
}

// Store is the delivered figures, on the brain's own handle.
type Store struct {
	sql      *sql.DB
	dialect  sqldialect.Dialect
	registry *rollout.Registry
	now      func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithRegistry lets the store name who approved a definition. Without it
// every approval is recorded as unchecked, not as unapproved.
func WithRegistry(r *rollout.Registry) Option { return func(s *Store) { s.registry = r } }

// WithClock fixes time for a test.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New takes the brain's handle and ensures the table.
func New(db *cortexdb.DB, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("reported: nil cortexdb.DB")
	}
	s := &Store{sql: db.SQL(), dialect: db.Dialect(), now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(s)
	}
	if _, err := s.sql.ExecContext(context.Background(), schema); err != nil {
		return nil, fmt.Errorf("reported: create schema: %w", err)
	}
	return s, nil
}

// schema is one statement both SQLite and PostgreSQL accept.
//
// The time is TEXT in RFC 3339 UTC rather than a timestamp type: it is only
// ever ordered and displayed, and a string sorts the same in both dialects
// where DATETIME is not even a PostgreSQL type. `by_name` because BY is a
// keyword. `supersedes` is UNIQUE so that two corrections racing for one
// report are decided by the database rather than by whichever read ran
// first; NULL for an original, and both dialects let NULLs repeat under
// UNIQUE.
const schema = `CREATE TABLE IF NOT EXISTS di_reported (
	name       TEXT PRIMARY KEY,
	at         TEXT NOT NULL,
	by_name    TEXT NOT NULL,
	supersedes TEXT UNIQUE,
	body       TEXT NOT NULL
)`

// Freezing is a delivered figure on its way into the record.
type Freezing struct {
	Name string
	By   string
	Note string

	// Supersedes names the report this one corrects. The original must
	// exist and must not already have a correction — correct the latest
	// one instead, so the chain stays a chain — and Note must say why.
	Supersedes string

	Question string
	Query    semantic.Query
	Who      governance.Principal
	Policy   governance.Policy

	Columns []string
	Rows    [][]any
	SQL     string

	// ModelHash is required: a figure whose definition cannot be named
	// cannot later be told apart from a figure whose data moved. Model, when
	// given, must hash to it.
	ModelHash string
	Model     []byte
}

// FromBrief fills what a brief already carries. The caller adds the name,
// the sender, the principal and policy it asked as, and — if it has the file
// — the model bytes.
func FromBrief(b *brief.Brief) Freezing {
	return Freezing{
		Question: b.Question, Query: b.Query,
		Columns: b.Columns, Rows: b.Rows, SQL: b.SQL, ModelHash: b.ModelHash,
	}
}

// FromAnswer fills what a governed answer carries, plus the engine's hash.
func FromAnswer(eng *engine.Engine, q semantic.Query, a *engine.Answer) Freezing {
	return Freezing{Query: q, Columns: a.Columns, Rows: a.Rows, SQL: a.SQL, ModelHash: eng.ModelHash}
}

// Freeze writes a delivered figure down. It refuses a name already used,
// whatever the new content: the refusal is the feature.
func (s *Store) Freeze(ctx context.Context, f Freezing) (*Report, error) {
	name := strings.TrimSpace(f.Name)
	by := strings.TrimSpace(f.By)
	switch {
	case name == "":
		return nil, fmt.Errorf("%w: a report needs a name somebody will quote back later", ErrInvalid)
	case by == "":
		return nil, fmt.Errorf("%w: a report nobody is named as sending cannot be argued with", ErrInvalid)
	case f.ModelHash == "":
		return nil, fmt.Errorf("%w: %s has no model hash — without one, a later change of definition would read as a change of data", ErrInvalid, name)
	case len(f.Model) > 0 && rollout.HashBytes(f.Model) != f.ModelHash:
		return nil, fmt.Errorf("%w: the model given hashes to %s, not %s — freezing it would record a definition that did not produce these figures",
			ErrInvalid, rollout.HashBytes(f.Model), f.ModelHash)
	case f.Supersedes != "" && strings.TrimSpace(f.Note) == "":
		return nil, fmt.Errorf("%w: correcting %s needs a reason in the note", ErrInvalid, f.Supersedes)
	}

	if _, err := s.Get(ctx, name); err == nil {
		return nil, fmt.Errorf("%w: %s — a report once sent does not change; freeze a correction under a new name that supersedes it", ErrExists, name)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if f.Supersedes != "" {
		prev, err := s.Get(ctx, f.Supersedes)
		if err != nil {
			return nil, err
		}
		if prev.SupersededBy != "" {
			return nil, fmt.Errorf("%w: %s was already corrected by %s — supersede that one", ErrSuperseded, prev.Name, prev.SupersededBy)
		}
	}

	rows := keepable(f.Rows)
	r := &Report{
		Name: name, At: s.now().UTC().Truncate(time.Microsecond), By: by, Note: f.Note,
		Supersedes: f.Supersedes, Question: f.Question, Query: f.Query, Who: f.Who,
		Columns: f.Columns, Rows: rows, SQL: f.SQL,
		ModelHash: f.ModelHash, Model: string(f.Model),
		Approval: s.approval(ctx, f.ModelHash),
		Policy:   f.Policy, PolicyHash: policyHash(f.Policy),
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("reported: %s: %w", name, err)
	}
	var sup any
	if f.Supersedes != "" {
		sup = f.Supersedes
	}
	if _, err := s.sql.ExecContext(ctx, s.dialect.Rebind(
		`INSERT INTO di_reported (name, at, by_name, supersedes, body) VALUES (?, ?, ?, ?, ?)`),
		name, r.At.Format(time.RFC3339Nano), by, sup, string(body)); err != nil {
		// Lost a race for the name or for the correction. The constraints
		// decided; say which in the words the reads above would have used.
		if _, e := s.Get(ctx, name); e == nil {
			return nil, fmt.Errorf("%w: %s", ErrExists, name)
		}
		if f.Supersedes != "" {
			if prev, e := s.Get(ctx, f.Supersedes); e == nil && prev.SupersededBy != "" {
				return nil, fmt.Errorf("%w: %s by %s", ErrSuperseded, prev.Name, prev.SupersededBy)
			}
		}
		return nil, fmt.Errorf("reported: freeze %s: %w", name, err)
	}
	return r, nil
}

// Get reads one report back, with the name of whatever corrected it.
func (s *Store) Get(ctx context.Context, name string) (*Report, error) {
	var body string
	err := s.sql.QueryRowContext(ctx, s.dialect.Rebind(`SELECT body FROM di_reported WHERE name = ?`), name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("reported: read %s: %w", name, err)
	}
	r, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("reported: %s: %w", name, err)
	}
	var next sql.NullString
	err = s.sql.QueryRowContext(ctx, s.dialect.Rebind(`SELECT name FROM di_reported WHERE supersedes = ?`), name).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reported: read %s: %w", name, err)
	}
	r.SupersededBy = next.String
	return r, nil
}

// List is every report, newest first. ORDER BY on both keys so two backends
// give one answer.
func (s *Store) List(ctx context.Context) ([]*Report, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT name FROM di_reported ORDER BY at DESC, name DESC`)
	if err != nil {
		return nil, fmt.Errorf("reported: list: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Report, 0, len(names))
	for _, n := range names {
		r, err := s.Get(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// approval asks the registry about a hash, recording a failure to ask as a
// failure rather than as an absence of signature.
func (s *Store) approval(ctx context.Context, hash string) Approval {
	if s.registry == nil || hash == "" {
		return Approval{}
	}
	a, found, err := s.registry.Signature(ctx, hash)
	if err != nil {
		return Approval{Checked: true, Err: err.Error()}
	}
	return Approval{Checked: true, Found: found, SignedBy: a.SignedBy, SignedAt: a.SignedAt, Note: a.Note, Promoted: a.Promoted}
}

// decode reads a body back with numbers as json.Number, so an integer count
// comes back as the integer that was sent and not as the nearest float64.
func decode(body string) (*Report, error) {
	var r Report
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, err
	}
	// The query goes through plain decoding above — filter values are what
	// the compiler binds, and it knows float64 and not json.Number. The rows
	// are decoded again, precisely.
	var rows struct {
		Rows [][]any `json:"rows"`
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		return nil, err
	}
	r.Rows = rows.Rows
	return &r, nil
}

// keepable copies rows into values JSON keeps faithfully. []byte would be
// written as base64 and read back as a different string; a NaN cannot be
// written at all. Both become the text a person would have seen.
func keepable(in [][]any) [][]any {
	out := make([][]any, len(in))
	for i, row := range in {
		r := make([]any, len(row))
		for j, v := range row {
			switch t := v.(type) {
			case []byte:
				r[j] = string(t)
			case float64:
				if math.IsNaN(t) || math.IsInf(t, 0) {
					r[j] = fmt.Sprint(t)
				} else {
					r[j] = t
				}
			default:
				r[j] = v
			}
		}
		out[i] = r
	}
	return out
}

// policyHash names an access policy the way a model hash names a model.
// encoding/json writes map keys sorted, so equal policies hash equal.
func policyHash(p governance.Policy) string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))[:12]
}
