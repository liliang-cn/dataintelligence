package warehouse

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/microsoft/go-mssqldb" // database/sql driver "sqlserver"
)

// OpenSQLServer opens a SQL Server (T-SQL) warehouse.
//
// SQL Server does have row-level security, but it is declared on the table with
// a security predicate reading SESSION_CONTEXT — a different mechanism from
// Postgres' set_config GUCs, and one this package does not wire up yet. Rather
// than accept an AppRole and enforce nothing, it is refused: an identity that
// looks propagated but is not is worse than one that is plainly absent.
func OpenSQLServer(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	if opts.AppRole != "" {
		return nil, fmt.Errorf(
			"warehouse: DI_DB_APP_ROLE is set, but SQL Server row-level security is not wired up here " +
				"(it needs SESSION_CONTEXT, not Postgres GUCs) — unset it, or use Postgres")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping warehouse: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = defaultMaxRows
	}
	return &Warehouse{db: db, opts: opts, driver: "sqlserver"}, nil
}
