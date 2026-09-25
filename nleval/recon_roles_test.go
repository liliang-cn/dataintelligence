package nleval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const gatedModel = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - {name: revenue, description: gross, synonyms: [r], entity: order_item, agg: sum, expr: "amount"}
  - name: net_revenue
    description: after refunds
    synonyms: [n]
    entity: order_item
    agg: sum
    expr: "amount - refund"
    roles: [cfo]
`

// A gated metric is exactly the one whose arithmetic most needs checking, and
// since the compiler learned to refuse a query with no roles it was the one
// the gate silently stopped checking: Meridian's net_revenue failed with
// "requires one of roles [finance admin] (caller has [])" and its control was
// never compared. The role here is `cfo`, not `admin`, so a fix that guessed a
// super-role would still fail.
func TestReconciliationChecksAGatedMetricsArithmetic(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "wh.db")
	wh, err := warehouse.OpenSQLite(ctx, path+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE order_items (id INTEGER PRIMARY KEY, amount REAL, refund REAL)`,
		`INSERT INTO order_items (amount, refund) VALUES (100, 10), (50, 0)`,
	} {
		if _, err := wh.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = wh.Close()

	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(gatedModel), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, mp, "sqlite://"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	rep, err := Reconcile(ctx, eng, &ReconSet{Cases: []ReconCase{
		{Metric: "net_revenue", Control: "SELECT SUM(amount - refund) FROM order_items"},
		{Metric: "revenue", Control: "SELECT SUM(amount) FROM order_items"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Error != "" {
			t.Errorf("%s was not reconciled: %s", r.Metric, r.Error)
			continue
		}
		if !r.Pass {
			t.Errorf("%s: got %v, want %v", r.Metric, r.Got, r.Want)
		}
	}
	if len(rep.Results) != 2 {
		t.Fatalf("got %d results", len(rep.Results))
	}
	if r := rep.Results[0]; r.Metric == "net_revenue" && strings.Contains(r.Error, "roles") {
		t.Errorf("the gate is refusing the metric instead of checking it: %s", r.Error)
	}
}
