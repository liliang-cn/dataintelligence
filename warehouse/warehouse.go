// Package warehouse executes compiled SQL against a real warehouse with cost
// guardrails (timeout + row cap) at the boundary. It does not compile SQL — the
// semantic-go compiler does that; this only runs it.
package warehouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
)

type Options struct {
	Timeout      time.Duration // per-query statement timeout (default 30s)
	MaxRows      int           // hard row cap (default 10000) — guardrail the agent can't override
	MaxScanBytes int64         // if >0, refuse a query whose planner estimate exceeds this (pre-execution cost ceiling)
	AppRole      string        // if set, QueryAs runs as this least-privilege role so RLS applies (superusers bypass RLS)
}

type Warehouse struct {
	db     *sql.DB
	opts   Options
	driver string // "pgx" (Postgres) | "duckdb"
}

type Result struct {
	Columns   []string
	Rows      [][]any
	RowCount  int
	Truncated bool
	SQL       string
	Elapsed   time.Duration
}

func OpenPostgres(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping warehouse: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 10000
	}
	return &Warehouse{db: db, opts: opts, driver: "pgx"}, nil
}

func (w *Warehouse) Close() error { return w.db.Close() }

// MaxScanBytes exposes the configured byte ceiling (0 = disabled).
func (w *Warehouse) MaxScanBytes() int64 { return w.opts.MaxScanBytes }

// Estimate asks the planner (EXPLAIN, no execution) for the estimated output
// size of a query: rows × per-row width. It runs nothing against the data, so
// it is safe to call as a pre-flight cost check.
func (w *Warehouse) Estimate(ctx context.Context, query string, args ...any) (rows int64, bytes int64, err error) {
	if w.driver == "duckdb" {
		return 0, 0, nil // DuckDB's EXPLAIN format differs; skip the byte-ceiling pre-flight
	}
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()
	var js []byte
	row := w.db.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+query, args...)
	if err = row.Scan(&js); err != nil {
		return 0, 0, fmt.Errorf("explain: %w", err)
	}
	// EXPLAIN (FORMAT JSON) → [ { "Plan": { "Plan Rows": N, "Plan Width": W, ... } } ]
	var plans []struct {
		Plan struct {
			Rows  int64 `json:"Plan Rows"`
			Width int64 `json:"Plan Width"`
		} `json:"Plan"`
	}
	if err = json.Unmarshal(js, &plans); err != nil || len(plans) == 0 {
		return 0, 0, fmt.Errorf("parse explain: %w", err)
	}
	rows = plans[0].Plan.Rows
	bytes = rows * plans[0].Plan.Width
	return rows, bytes, nil
}

// GuardCost refuses a query whose estimated output exceeds MaxScanBytes, before
// a single row is read. A no-op when the ceiling is disabled. This is the
// pre-execution half of the cost guardrails (the timeout + row cap are the
// during/after halves).
func (w *Warehouse) GuardCost(ctx context.Context, query string, args ...any) error {
	if w.opts.MaxScanBytes <= 0 {
		return nil
	}
	rows, bytes, err := w.Estimate(ctx, query, args...)
	if err != nil {
		return err
	}
	if bytes > w.opts.MaxScanBytes {
		return fmt.Errorf("query refused: estimated %d bytes (%d rows) exceeds the %d-byte ceiling — add a filter or narrow the grain", bytes, rows, w.opts.MaxScanBytes)
	}
	return nil
}

// Exec runs a statement (DDL / INSERT) under the timeout and returns rows affected.
func (w *Warehouse) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()
	res, err := w.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Apply runs fn inside a single transaction (BEGIN → fn → COMMIT; ROLLBACK on
// any error). It is the atomic write path used by the write-back engine: snapshot
// the before-image, mutate, and audit all commit together or not at all.
func (w *Warehouse) Apply(ctx context.Context, fn func(*sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (w *Warehouse) Query(ctx context.Context, query string, args ...any) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()

	start := time.Now()
	rows, err := w.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return w.scan(rows, query, start)
}

// Session is the propagated end-user identity (the result of an on-behalf-of
// token exchange). QueryAs binds it to the DB session so the warehouse's own
// row-level-security policies enforce on the real user — identity travels all
// the way to the data, not just the app layer.
type Session struct {
	User   string
	Role   string
	Tenant string
	Region string
}

// QueryAs runs a read-only query inside a transaction whose request-local GUCs
// (app.user / app.role / app.tenant / app.region) carry the end-user identity.
// Postgres RLS policies read these via current_setting('app.region', true), so
// scoping is enforced by the engine — belt-and-suspenders under the app layer.
// The settings are transaction-local (set_config(..., true)), so they reset when
// the tx ends and never leak across pooled connections.
func (w *Warehouse) QueryAs(ctx context.Context, s Session, query string, args ...any) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()

	start := time.Now()
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; rollback also resets GUCs

	// Drop to a least-privilege role so RLS actually applies — a superuser (the
	// default local role) bypasses row security entirely. Transaction-local.
	if w.opts.AppRole != "" {
		if !safeIdent(w.opts.AppRole) {
			return nil, fmt.Errorf("invalid app role %q", w.opts.AppRole)
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE "`+w.opts.AppRole+`"`); err != nil {
			return nil, fmt.Errorf("set role: %w", err)
		}
	}
	for _, kv := range [][2]string{
		{"app.user", s.User}, {"app.role", s.Role}, {"app.tenant", s.Tenant}, {"app.region", s.Region},
	} {
		if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("set %s: %w", kv[0], err)
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return w.scan(rows, query, start)
}

// safeIdent guards the role name used in SET LOCAL ROLE (which can't be a bind param).
func safeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// scan materializes a result set under the row-cap guardrail.
func (w *Warehouse) scan(rows *sql.Rows, query string, start time.Time) (*Result, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols, SQL: query}
	for rows.Next() {
		if len(res.Rows) >= w.opts.MaxRows {
			res.Truncated = true
			break
		}
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, holders)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	res.Elapsed = time.Since(start)
	return res, nil
}
