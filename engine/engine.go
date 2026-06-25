// Package engine is the query spine: it ties the semantic model + compiler
// (semantic-go) to a real warehouse. Given a semantic Query it compiles
// fanout/chasm-safe SQL and executes it under guardrails.
package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
	semantic "github.com/liliang-cn/semantic-go"
)

type Engine struct {
	Model   *semantic.Model
	WH      *warehouse.Warehouse
	Dialect semantic.Dialect
}

// New loads a semantic model from YAML and opens the warehouse. When
// DI_DB_APP_ROLE is set, governed queries run as that least-privilege role so the
// warehouse's own RLS engages (on-behalf-of, belt-and-suspenders).
func New(ctx context.Context, modelPath, dsn string) (*Engine, error) {
	m, err := semantic.LoadFile(modelPath)
	if err != nil {
		return nil, err
	}
	wh, err := warehouse.OpenPostgres(ctx, dsn, warehouse.Options{AppRole: os.Getenv("DI_DB_APP_ROLE")})
	if err != nil {
		return nil, err
	}
	return &Engine{Model: m, WH: wh, Dialect: semantic.Postgres{}}, nil
}

func (e *Engine) Close() error { return e.WH.Close() }

// Answer carries the result plus the compiled SQL and per-stage timings.
type Answer struct {
	Columns   []string
	Rows      [][]any
	SQL       string
	CompileMs int64
	ExecMs    int64
	TraceID   string // set by the governance layer when it traces the request
}

// Query compiles a semantic query and runs it, timing each stage.
func (e *Engine) Query(ctx context.Context, q semantic.Query) (*Answer, error) {
	return e.run(ctx, q, nil)
}

// QueryAs is Query under a propagated end-user session: the SQL runs with the
// caller's identity bound to the DB session (app.* GUCs), so the warehouse's RLS
// policies enforce on the real user.
func (e *Engine) QueryAs(ctx context.Context, q semantic.Query, sess warehouse.Session) (*Answer, error) {
	return e.run(ctx, q, &sess)
}

func (e *Engine) run(ctx context.Context, q semantic.Query, sess *warehouse.Session) (*Answer, error) {
	t0 := time.Now()
	compiled, err := semantic.Compile(e.Model, q, e.Dialect)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	compileMs := time.Since(t0).Milliseconds()
	var res *warehouse.Result
	if sess != nil {
		res, err = e.WH.QueryAs(ctx, *sess, compiled.SQL, compiled.Args...)
	} else {
		res, err = e.WH.Query(ctx, compiled.SQL, compiled.Args...)
	}
	if err != nil {
		return nil, err
	}
	return &Answer{
		Columns: res.Columns, Rows: res.Rows, SQL: compiled.SQL,
		CompileMs: compileMs, ExecMs: res.Elapsed.Milliseconds(),
	}, nil
}
