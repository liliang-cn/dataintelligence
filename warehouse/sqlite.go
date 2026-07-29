package warehouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go database/sql driver "sqlite" (no cgo)
)

// OpenSQLite opens a SQLite file as a warehouse, read-only.
//
// SQLite is the shape a lot of real data arrives in: a single file exported
// from a desktop system. Governance that only exists in Go still applies
// (metric RBAC, masking, k-anonymity); there is no row-level security to
// enforce, so an AppRole is refused rather than silently ignored.
//
// The connection is opened read-only at the driver level, not by convention —
// a governed reader has no business being able to write, and "we only send
// SELECTs" is a claim about code rather than a property of the connection.
func OpenSQLite(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	if opts.AppRole != "" {
		return nil, fmt.Errorf(
			"warehouse: DI_DB_APP_ROLE is set, but SQLite has no row-level security to enforce it — " +
				"unset it, or use Postgres if per-user row scoping must be enforced by the database")
	}
	db, err := sql.Open("sqlite", SQLiteDSN(dsn))
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
	return &Warehouse{db: db, opts: opts, driver: "sqlite"}, nil
}

// SQLiteDSN turns the DSN shapes a config or UI produces into a file path with
// read-only mode set:
//
//	sqlite:///data/shop.db · sqlite:/data/shop.db · /data/shop.db
//
// An explicit mode= in the DSN is left alone, so a caller that deliberately
// wants a writable handle (the ingest path) still gets one.
func SQLiteDSN(dsn string) string {
	path := dsn
	for _, p := range []string{"sqlite3://", "sqlite://", "sqlite3:", "sqlite:"} {
		if strings.HasPrefix(path, p) {
			path = strings.TrimPrefix(path, p)
			break
		}
	}
	if strings.Contains(path, "mode=") {
		return path
	}
	if strings.Contains(path, "?") {
		return path + "&mode=ro"
	}
	return path + "?mode=ro"
}
