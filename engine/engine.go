// Package engine is the query spine: it ties the semantic model + compiler
// (semantic-go) to a real warehouse. Given a semantic Query it compiles
// fanout/chasm-safe SQL and executes it under guardrails.
package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/liliang-cn/dataintelligence/obs"
	"github.com/liliang-cn/dataintelligence/warehouse"
	semantic "github.com/liliang-cn/semantic-go"
	"go.opentelemetry.io/otel/attribute"
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
	wh, err := warehouse.OpenPostgres(ctx, dsn, warehouse.Options{
		AppRole:      os.Getenv("DI_DB_APP_ROLE"),
		MaxScanBytes: envBytes("DI_MAX_SCAN_BYTES"), // 0 = disabled
	})
	if err != nil {
		return nil, err
	}
	return &Engine{Model: m, WH: wh, Dialect: semantic.Postgres{}}, nil
}

func (e *Engine) Close() error { return e.WH.Close() }

// envBytes reads a byte budget from the environment (plain integer bytes),
// returning 0 (disabled) when unset or unparseable.
func envBytes(key string) int64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	var n int64
	if _, err := fmt.Sscan(v, &n); err != nil || n < 0 {
		return 0
	}
	return n
}

// Answer carries the result plus the compiled SQL and per-stage timings.
type Answer struct {
	Columns   []string
	Rows      [][]any
	SQL       string
	CompileMs int64
	ExecMs    int64
	EstRows   int64  // planner's estimated output rows (cost proxy)
	EstBytes  int64  // planner's estimated output bytes (cost proxy beyond latency)
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
	_, cspan := obs.Tracer().Start(ctx, "compile")
	compiled, err := semantic.Compile(e.Model, q, e.Dialect)
	cspan.End()
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	compileMs := time.Since(t0).Milliseconds()
	// Pre-execution cost estimate (one EXPLAIN): used both as the byte-ceiling
	// guardrail and as a cost signal recorded on the trace. Best-effort — if the
	// planner can't estimate, fall back to executing without a ceiling.
	pctx, pspan := obs.Tracer().Start(ctx, "plan")
	estRows, estBytes, estErr := e.WH.Estimate(pctx, compiled.SQL, compiled.Args...)
	pspan.SetAttributes(attribute.Int64("est_rows", estRows), attribute.Int64("est_bytes", estBytes))
	pspan.End()
	if estErr == nil && e.WH.MaxScanBytes() > 0 && estBytes > e.WH.MaxScanBytes() {
		return nil, fmt.Errorf("query refused: estimated %d bytes (%d rows) exceeds the %d-byte ceiling — add a filter or narrow the grain", estBytes, estRows, e.WH.MaxScanBytes())
	}
	ectx, espan := obs.Tracer().Start(ctx, "execute")
	var res *warehouse.Result
	if sess != nil {
		res, err = e.WH.QueryAs(ectx, *sess, compiled.SQL, compiled.Args...)
	} else {
		res, err = e.WH.Query(ectx, compiled.SQL, compiled.Args...)
	}
	espan.End()
	if err != nil {
		return nil, err
	}
	return &Answer{
		Columns: res.Columns, Rows: res.Rows, SQL: compiled.SQL,
		CompileMs: compileMs, ExecMs: res.Elapsed.Milliseconds(),
		EstRows: estRows, EstBytes: estBytes,
	}, nil
}
