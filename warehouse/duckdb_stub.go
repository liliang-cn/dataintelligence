//go:build !duckdb

package warehouse

import (
	"context"
	"errors"
)

// OpenDuckDB is unavailable in the default pure-Go static build. Rebuild with
// `-tags duckdb` (CGO_ENABLED=1) to enable the DuckDB execution backend.
func OpenDuckDB(_ context.Context, _ string, _ Options) (*Warehouse, error) {
	return nil, errors.New("DuckDB backend not compiled in; rebuild with -tags duckdb")
}
