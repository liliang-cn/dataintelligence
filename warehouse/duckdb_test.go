//go:build duckdb

package warehouse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

func cell(v any) string { return fmt.Sprintf("%v", v) }

// Run with: CGO_ENABLED=1 go test -tags duckdb ./warehouse/

func TestDuckDBInMemoryQuery(t *testing.T) {
	ctx := context.Background()
	wh, err := OpenDuckDB(ctx, ":memory:", Options{})
	if err != nil {
		t.Fatalf("OpenDuckDB: %v", err)
	}
	defer wh.Close()

	if _, err := wh.Exec(ctx, `CREATE TABLE order_items(qty int, price double)`); err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `INSERT INTO order_items VALUES (2,10.00),(3,5.00),(1,20.00)`); err != nil {
		t.Fatal(err)
	}
	// Run SQL shaped by the DuckDB dialect (date_trunc/quoting/$N all valid here).
	res, err := wh.Query(ctx, `SELECT sum(qty*price) AS revenue FROM "order_items"`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := cell(res.Rows[0][0]); !strings.HasPrefix(got, "55") {
		t.Errorf("revenue = %q, want 55", got)
	}

	// Estimate is skipped on DuckDB (different EXPLAIN) — must not error.
	if _, _, err := wh.Estimate(ctx, `SELECT 1`); err != nil {
		t.Errorf("Estimate should no-op on duckdb, got %v", err)
	}
}

// DuckDB's signature: query a CSV/Parquet file in-process, no server, no load.
func TestDuckDBQueriesCSVInPlace(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "sales.csv")
	if err := os.WriteFile(csv, []byte("region,amount\nWest,100\nEast,250\nWest,150\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	wh, err := OpenDuckDB(ctx, ":memory:", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer wh.Close()

	res, err := wh.Query(ctx,
		`SELECT region, sum(amount) AS total FROM read_csv_auto(`+quote(csv)+`) GROUP BY region ORDER BY region`)
	if err != nil {
		t.Fatalf("read_csv_auto: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("want 2 regions, got %d", len(res.Rows))
	}
	if cell(res.Rows[0][0]) != "East" || cell(res.Rows[1][0]) != "West" {
		t.Errorf("unexpected regions: %v", res.Rows)
	}
}

// Sanity: the DuckDB dialect compiles a semantic query without error.
func TestDuckDBDialectCompiles(t *testing.T) {
	if (semantic.DuckDB{}).Name() != "duckdb" {
		t.Fatal("dialect name")
	}
}

func quote(s string) string { return "'" + s + "'" }
