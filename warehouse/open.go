package warehouse

import (
	"context"
	"strings"
)

// Open picks the backend from the DSN scheme:
//
//	postgres:// postgresql://   Postgres   (also the default for an unprefixed DSN)
//	mysql://    mariadb://      MySQL / MariaDB
//	sqlite://   sqlite3://      SQLite file
//	sqlserver:// mssql://       SQL Server
//	duckdb:                     DuckDB (requires -tags duckdb)
//
// Every entry point that takes a -dsn goes through here. When each command
// called OpenPostgres directly, adding an engine meant remembering ten call
// sites, and the ones that were forgotten failed with a Postgres parse error
// about a DSN that was never Postgres to begin with.
func Open(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	switch {
	case strings.HasPrefix(dsn, "duckdb:"):
		path := strings.TrimPrefix(dsn, "duckdb:")
		if path == "" {
			path = ":memory:"
		}
		return OpenDuckDB(ctx, path, opts)
	case hasScheme(dsn, "mysql", "mariadb"):
		return OpenMySQL(ctx, dsn, opts)
	case hasScheme(dsn, "sqlite", "sqlite3"):
		return OpenSQLite(ctx, dsn, opts)
	case hasScheme(dsn, "sqlserver", "mssql"):
		return OpenSQLServer(ctx, dsn, opts)
	default:
		return OpenPostgres(ctx, dsn, opts)
	}
}

func hasScheme(dsn string, schemes ...string) bool {
	for _, s := range schemes {
		if strings.HasPrefix(dsn, s+"://") || strings.HasPrefix(dsn, s+":/") {
			return true
		}
	}
	return false
}
