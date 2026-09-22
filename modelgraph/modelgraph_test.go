package modelgraph

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	semantic "github.com/liliang-cn/semantic-go"
)

// A model shaped like the real one this was built against: two sides that
// count their own yield differently, and a third metric that reconciles them.
const yaml = `
entities:
  - {name: pouring,  table: pouring,  primary_key: id}
  - {name: cleaning, table: cleaning, primary_key: id}
  - {name: batch,    table: batch,    primary_key: batch_id}
joins:
  - {from: cleaning, to: batch, from_key: batch_id, to_key: batch_id, cardinality: many_to_one}
  - {from: pouring,  to: batch, from_key: batch_id, to_key: batch_id, cardinality: many_to_one}
dimensions:
  - {name: plant, entity: batch, column: plant, type: categorical}
metrics:
  - {name: poured_qty,   description: d, entity: pouring,  agg: sum, expr: "poured_qty"}
  - {name: cleaned_qty,  description: d, entity: cleaning, agg: sum, expr: "out_qty"}
  - {name: rework_qty,   description: d, entity: cleaning, agg: sum, expr: "rework_qty"}
  - {name: yield,        description: d, formula: "cleaned_qty / nullif(poured_qty, 0)"}
  - {name: net_yield,    description: d, formula: "(cleaned_qty - rework_qty) / nullif(poured_qty, 0)"}
  - {name: yield_ytd,    description: d, of: yield, window: cumulative, reset: year}
`

func build(t *testing.T) (*cortexdb.DB, *semantic.Model) {
	t.Helper()
	m, err := semantic.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	cfg := cortexdb.DefaultConfig(filepath.Join(t.TempDir(), "brain.db"))
	cfg.Dimensions = 1
	db, err := cortexdb.Open(cfg)
	if err != nil {
		t.Fatalf("open brain: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := Build(context.Background(), db.Graph(), m, "abc123", db.Info().Dimensions); err != nil {
		t.Fatalf("build: %v", err)
	}
	return db, m
}

// Every edge in this graph traces to a line somebody wrote. The derivation
// edges are the ones worth checking, because they are the only ones that are
// read out of an expression rather than out of a field.
func TestAFormulaBecomesEdgesToTheMetricsItNames(t *testing.T) {
	db, _ := build(t)
	got, err := Explain(context.Background(), db.Graph(), "net_yield", 1)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	metrics := map[string]bool{}
	for _, d := range got {
		if d.Kind == "metric" {
			metrics[d.Name] = true
		}
	}
	for _, want := range []string{"cleaned_qty", "rework_qty", "poured_qty"} {
		if !metrics[want] {
			t.Errorf("net_yield does not derive from %q; got %v", want, metrics)
		}
	}
	// `nullif` is a function, not a metric. A graph that contains invented
	// nodes is worse than no graph.
	if metrics["nullif"] {
		t.Error("a SQL function became a metric node")
	}
}

// A window metric names exactly one metric, in Of.
func TestAWindowMetricDerivesFromTheMetricItTransforms(t *testing.T) {
	db, _ := build(t)
	got, err := Explain(context.Background(), db.Graph(), "yield_ytd", 1)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range got {
		if d.Kind == "metric" && d.Name == "yield" {
			found = true
			if d.Via != DerivesFrom {
				t.Errorf("via = %q, want %q", d.Via, DerivesFrom)
			}
		}
	}
	if !found {
		t.Errorf("yield_ytd does not point at yield: %+v", got)
	}
}

// The question a model file cannot answer: this figure is wrong, what did it
// touch? Two hops out, through a definition nobody remembers.
func TestExplainReachesThroughADefinitionToItsColumns(t *testing.T) {
	db, _ := build(t)
	got, err := Explain(context.Background(), db.Graph(), "net_yield", 4)
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]int{}
	for _, d := range got {
		if d.Kind == "column" {
			cols[d.Name] = d.Depth
		}
	}
	for _, want := range []string{"cleaning.out_qty", "cleaning.rework_qty", "pouring.poured_qty"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("net_yield never reaches %q; reached %v", want, cols)
		}
	}
	// net_yield reads no column itself — it is a formula. Everything it
	// reaches is at least two hops away, and saying so is the point of
	// reporting depth at all.
	for name, d := range cols {
		if d < 2 {
			t.Errorf("%s reported at depth %d, but net_yield reads no column directly", name, d)
		}
	}
}

// The mirror image, asked by whoever is about to run the migration.
func TestImpactNamesTheMetricsAColumnChangeWouldMove(t *testing.T) {
	db, _ := build(t)
	got, err := Impact(context.Background(), db.Graph(), "cleaning.out_qty", 4)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	hops := map[string]int{}
	for _, a := range got {
		hops[a.Metric] = a.Hops
	}
	if hops["cleaned_qty"] != 1 {
		t.Errorf("cleaned_qty reads the column directly, reported at %d hop(s): %+v", hops["cleaned_qty"], got)
	}
	// These are the ones nobody remembers.
	for _, want := range []string{"yield", "net_yield"} {
		if hops[want] < 2 {
			t.Errorf("%s derives from a metric that reads the column, but impact reports %d: %+v",
				want, hops[want], got)
		}
	}
	// And three hops out, the year-to-date view of it.
	if hops["yield_ytd"] < 3 {
		t.Errorf("yield_ytd is three definitions from the column, reported at %d: %+v", hops["yield_ytd"], got)
	}
	if len(got) != 4 {
		t.Errorf("impact = %+v, want exactly the four metrics that move", got)
	}
}

// A misspelled name and an empty answer must not look the same.
func TestAnUnknownNameIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	db, _ := build(t)
	if _, err := Explain(context.Background(), db.Graph(), "revenu", 2); err == nil {
		t.Fatal("a misspelled metric returned an empty explanation instead of an error")
	} else if !strings.Contains(err.Error(), "revenu") {
		t.Errorf("the error does not name what was not found: %v", err)
	}
	if _, err := Impact(context.Background(), db.Graph(), "cleaning.nope", 2); err == nil {
		t.Fatal("an unknown column returned an empty impact instead of an error")
	}
}

// Re-projecting the same model must not double the graph.
func TestRebuildingIsIdempotent(t *testing.T) {
	db, m := build(t)
	first, err := Build(context.Background(), db.Graph(), m, "abc123", db.Info().Dimensions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), db.Graph(), m, "abc123", db.Info().Dimensions)
	if err != nil {
		t.Fatal(err)
	}
	if first.Nodes != second.Nodes || first.Edges != second.Edges {
		t.Errorf("rebuild wrote %d/%d then %d/%d", first.Nodes, first.Edges, second.Nodes, second.Edges)
	}
	got, err := Explain(context.Background(), db.Graph(), "net_yield", 1)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, d := range got {
		seen[d.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times after three projections", name, n)
		}
	}
}
