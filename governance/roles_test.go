package governance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/warehouse"
	semantic "github.com/liliang-cn/semantic-go"
)

const gatedModel = `
entities:
  - {name: order_line, table: order_line, primary_key: line_id}
dimensions: []
metrics:
  - {name: units,   description: d, synonyms: [u], entity: order_line, agg: sum, expr: "qty"}
  - {name: revenue, description: d, synonyms: [r], entity: order_line, agg: sum, expr: "qty*price", roles: [finance, admin]}
`

func gatedEngine(t *testing.T) *engine.Engine {
	t.Helper()
	dir := t.TempDir()
	whPath := filepath.Join(dir, "wh.db")
	wh, err := warehouse.OpenSQLite(context.Background(), whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(context.Background(),
		`CREATE TABLE order_line (line_id INTEGER PRIMARY KEY, qty INTEGER, price REAL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(context.Background(),
		`INSERT INTO order_line (qty, price) VALUES (2, 10.0), (3, 5.0)`); err != nil {
		t.Fatal(err)
	}
	_ = wh.Close()

	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(gatedModel), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(context.Background(), mp, "sqlite://"+whPath+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// A gate that refuses everybody is not a gate, it is an outage.
//
// The compiler grew its own role check, reading semantic.Query.Roles, and this
// package filled the Principal but never that field — so every metric carrying
// `roles:` was refused for every caller, including the ones it was meant for,
// with a message that named the roles it wanted and an empty list of the ones
// the caller had. Six metrics in the shipped model carry `roles:`.
func TestAGatedMetricIsAnsweredForTheRoleItIsGatedTo(t *testing.T) {
	eng := gatedEngine(t)
	ans, err := Query(context.Background(), eng,
		semantic.Query{Metrics: []string{"revenue"}},
		Principal{User: "cli", Role: "finance"}, DefaultPolicy())
	if err != nil {
		t.Fatalf("finance was refused its own metric: %v", err)
	}
	if len(ans.Rows) != 1 {
		t.Fatalf("no figure came back: %+v", ans.Rows)
	}
}

// And still refused for everyone else, by both gates.
func TestAGatedMetricIsStillRefusedForOtherRoles(t *testing.T) {
	eng := gatedEngine(t)
	_, err := Query(context.Background(), eng,
		semantic.Query{Metrics: []string{"revenue"}},
		Principal{User: "cli", Role: "analyst"}, DefaultPolicy())
	if err == nil {
		t.Fatal("an analyst got a metric gated to finance")
	}
	if !strings.Contains(err.Error(), "revenue") {
		t.Errorf("the refusal does not name the metric: %v", err)
	}
}

// An ungated metric is unaffected by any of this.
func TestAnUngatedMetricNeedsNoRole(t *testing.T) {
	eng := gatedEngine(t)
	if _, err := Query(context.Background(), eng,
		semantic.Query{Metrics: []string{"units"}},
		Principal{User: "cli", Role: ""}, DefaultPolicy()); err != nil {
		t.Fatalf("an ungated metric was refused: %v", err)
	}
}

// A caller with no role must not match a model that gated something to "".
func TestNoRoleIsAnEmptyListNotAListContainingEmpty(t *testing.T) {
	if got := rolesOf(Principal{Role: "  "}); len(got) != 0 {
		t.Errorf("rolesOf(blank) = %v, want empty", got)
	}
	if got := rolesOf(Principal{Role: "finance"}); len(got) != 1 || got[0] != "finance" {
		t.Errorf("rolesOf(finance) = %v", got)
	}
}
