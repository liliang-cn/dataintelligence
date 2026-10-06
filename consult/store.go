package consult

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver "sqlite"
)

// Store is the loop's ledger: one SQLite file, one table, append-only.
//
// A decision cannot be changed — to change your mind you propose a new plan.
// An acceptance cannot be changed either. The value of this table is "how was
// it judged at the time", and a record that can be edited afterwards cannot
// answer that. The one row that is replaced is a plan's latest progress check,
// which is explicitly not a verdict.
type Store struct {
	db         *sql.DB
	engagement string
}

const ddl = `CREATE TABLE IF NOT EXISTS consult (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	engagement TEXT NOT NULL,
	kind       TEXT NOT NULL,
	id         TEXT NOT NULL,
	parent     TEXT NOT NULL DEFAULT '',
	actor      TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT '',
	at         TEXT NOT NULL,
	doc        TEXT NOT NULL,
	UNIQUE (engagement, kind, id))`

// OpenStore opens (creating if needed) the ledger at path. engagement scopes
// every row to one customer, so one file can be handed to that customer.
func OpenStore(path, engagement string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway, this avoids SQLITE_BUSY
	if _, err := db.Exec(ddl); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("consult store %s: %w", path, err)
	}
	return &Store{db: db, engagement: engagement}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Kinds of rows.
const (
	kGoal       = "goal"
	kFinding    = "finding"
	kPlan       = "plan"
	kDecision   = "decision"
	kActionRun  = "action_run"
	kAcceptance = "acceptance"
	kProgress   = "progress"
)

var errDuplicate = errors.New("duplicate")

func (s *Store) put(ctx context.Context, kind, id, parent, actor, state string, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO consult (engagement, kind, id, parent, actor, state, at, doc) VALUES (?,?,?,?,?,?,?,?)`,
		s.engagement, kind, id, parent, actor, state, time.Now().UTC().Format(time.RFC3339Nano), string(b))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return errDuplicate
	}
	return err
}

// replace is only for progress rows — see the type comment.
func (s *Store) replace(ctx context.Context, kind, id, parent string, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO consult (engagement, kind, id, parent, at, doc) VALUES (?,?,?,?,?,?)
		 ON CONFLICT (engagement, kind, id) DO UPDATE SET at = excluded.at, doc = excluded.doc`,
		s.engagement, kind, id, parent, time.Now().UTC().Format(time.RFC3339Nano), string(b))
	return err
}

func (s *Store) get(ctx context.Context, kind, id string, out any) (bool, error) {
	var doc string
	err := s.db.QueryRowContext(ctx,
		`SELECT doc FROM consult WHERE engagement = ? AND kind = ? AND id = ?`, s.engagement, kind, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A row that cannot be read is an error, not a skip: a silently missing
	// decision makes an adopted plan look undecided.
	if err := json.Unmarshal([]byte(doc), out); err != nil {
		return false, fmt.Errorf("%s %s: %w", kind, id, err)
	}
	return true, nil
}

// list returns every doc of a kind (optionally under one parent), oldest first.
func list[T any](ctx context.Context, s *Store, kind, parent string) ([]T, error) {
	q := `SELECT kind, id, doc FROM consult WHERE engagement = ? AND kind = ?`
	args := []any{s.engagement, kind}
	if parent != "" {
		q += ` AND parent = ?`
		args = append(args, parent)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var k, id, doc string
		if err := rows.Scan(&k, &id, &doc); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, fmt.Errorf("%s %s: %w", k, id, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// nextID returns the next free id for a kind: g1, g2, … / f1 … / p1 ….
func (s *Store) nextID(ctx context.Context, kind string) (string, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM consult WHERE engagement = ? AND kind = ?`, s.engagement, kind).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%c%d", kind[0], n+1), nil
}

// insertWithID assigns the next id and inserts, retrying on a race.
func (s *Store) insertWithID(ctx context.Context, kind, parent, actor string, setID func(string) any) (string, error) {
	for range 5 {
		id, err := s.nextID(ctx, kind)
		if err != nil {
			return "", err
		}
		err = s.put(ctx, kind, id, parent, actor, "", setID(id))
		if errors.Is(err, errDuplicate) {
			continue
		}
		return id, err
	}
	return "", fmt.Errorf("could not allocate a %s id", kind)
}

func (s *Store) Goal(ctx context.Context, id string) (*Goal, error) {
	var g Goal
	ok, err := s.get(ctx, kGoal, id, &g)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse(RuleNotFound, "没有目标 %q", id)
	}
	return &g, nil
}

func (s *Store) Plan(ctx context.Context, id string) (*Plan, error) {
	var p Plan
	ok, err := s.get(ctx, kPlan, id, &p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse(RuleNotFound, "没有计划 %q", id)
	}
	return &p, nil
}

func (s *Store) Decision(ctx context.Context, plan string) (*Decision, error) {
	var d Decision
	ok, err := s.get(ctx, kDecision, plan, &d)
	if !ok || err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) Acceptance(ctx context.Context, plan string) (*Acceptance, error) {
	var a Acceptance
	ok, err := s.get(ctx, kAcceptance, plan, &a)
	if !ok || err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) Progress(ctx context.Context, plan string) (*Acceptance, error) {
	var a Acceptance
	ok, err := s.get(ctx, kProgress, plan, &a)
	if !ok || err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) Goals(ctx context.Context) ([]Goal, error) { return list[Goal](ctx, s, kGoal, "") }
func (s *Store) Findings(ctx context.Context) ([]Finding, error) {
	return list[Finding](ctx, s, kFinding, "")
}
func (s *Store) Plans(ctx context.Context) ([]Plan, error) { return list[Plan](ctx, s, kPlan, "") }
func (s *Store) ActionRuns(ctx context.Context, plan string) ([]ActionRun, error) {
	return list[ActionRun](ctx, s, kActionRun, plan)
}

func (s *Store) findingIDs(ctx context.Context) ([]string, error) {
	fs, err := s.Findings(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids, nil
}
