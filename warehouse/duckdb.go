//go:build duckdb

// DuckDB execution backend. Opt-in: it pulls in the CGO go-duckdb driver, so it
// is excluded from the default pure-Go static build. Build with `-tags duckdb`
// (CGO_ENABLED=1) to run queries against a DuckDB file or :memory: — ideal for
// querying Parquet/CSV in-process without a server.
package warehouse

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb" // registers the "duckdb" database/sql driver
)

// OpenDuckDB opens a DuckDB database (path, or ":memory:") with the same
// guardrails as the Postgres warehouse. The planner byte-ceiling is skipped
// (DuckDB EXPLAIN differs); RLS/OBO is Postgres-only.
func OpenDuckDB(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping duckdb: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 10000
	}
	return &Warehouse{db: db, opts: opts, driver: "duckdb"}, nil
}
