// Package warehouse executes compiled SQL against a real warehouse with cost
// guardrails (timeout + row cap) at the boundary. It does not compile SQL — the
// semantic-go compiler does that; this only runs it.
package warehouse

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
)

type Options struct {
	Timeout time.Duration // per-query statement timeout (default 30s)
	MaxRows int           // hard row cap (default 10000) — guardrail the agent can't override
	AppRole string        // if set, QueryAs runs as this least-privilege role so RLS applies (superusers bypass RLS)
}

type Warehouse struct {
	db   *sql.DB
	opts Options
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
	return &Warehouse{db: db, opts: opts}, nil
}

func (w *Warehouse) Close() error { return w.db.Close() }

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
