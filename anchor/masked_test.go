package anchor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// A masked dimension is not a scope. Grouped by, every value collapses into the
// mask literal and reproduces the whole-warehouse total — which made Meridian's
// revenue look ambiguous between "the whole warehouse" and "customer_email =
// ***", a scope the compiler would refuse to filter on.
func TestAMaskedDimensionIsNotOfferedAsAScope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "wh.db")
	wh, err := warehouse.OpenSQLite(ctx, path+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT, region TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER, amount REAL)`,
		`INSERT INTO customers VALUES (1,'a@x','East'),(2,'b@x','West')`,
		`INSERT INTO orders VALUES (1,1,100),(2,2,50)`,
	} {
		if _, err := wh.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = wh.Close()
	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(`
entities:
  - {name: customer, table: customers, primary_key: id}
  - {name: order, table: orders, primary_key: id}
joins:
  - {from: order, to: customer, from_key: customer_id, to_key: id, cardinality: many_to_one}
dimensions:
  - {name: customer_email, entity: customer, column: email, type: categorical, mask: "'***'", roles: [admin]}
  - {name: customer_region, entity: customer, column: region, type: categorical}
metrics:
  - {name: revenue, description: d, entity: order, agg: sum, expr: amount}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, mp, "sqlite://"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	r, err := Search(ctx, eng, "revenue", 150, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 1 {
		t.Fatalf("the total anchored to %d scopes, want exactly the whole warehouse: %+v", len(r.Matches), r.Matches)
	}
}
