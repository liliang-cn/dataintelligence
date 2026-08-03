package modelgen

import (
	"context"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

func sampleSchema() *Schema {
	return &Schema{Tables: []Table{
		{
			Name: "orders", PrimaryKey: "order_id",
			Columns: []Column{
				{Name: "order_id", Type: "bigint"},
				{Name: "store_id", Type: "integer"},
				{Name: "status", Type: "character varying"},
				{Name: "order_date", Type: "timestamp without time zone"},
			},
			ForeignKeys: []ForeignKey{{Column: "store_id", RefTable: "stores", RefColumn: "store_id"}},
		},
		{
			Name: "order_items", PrimaryKey: "item_id",
			Columns: []Column{
				{Name: "item_id", Type: "bigint"},
				{Name: "order_id", Type: "bigint"},
				{Name: "quantity", Type: "integer"},
				{Name: "unit_price", Type: "numeric"},
			},
			ForeignKeys: []ForeignKey{{Column: "order_id", RefTable: "orders", RefColumn: "order_id"}},
		},
		{
			Name: "stores", PrimaryKey: "store_id",
			Columns: []Column{
				{Name: "store_id", Type: "integer"},
				{Name: "region", Type: "text"},
			},
		},
	}}
}

// The heuristic model singularizes entities, derives joins from FKs, classifies
// columns, and produces a model that compiles fanout-safe SQL.
func TestHeuristicModel(t *testing.T) {
	m, err := HeuristicModel(sampleSchema())
	if err != nil {
		t.Fatalf("HeuristicModel: %v", err)
	}

	if m.Entity("order") == nil || m.Entity("order_item") == nil || m.Entity("store") == nil {
		t.Fatalf("entities not singularized: %+v", entityNames(m))
	}
	// FK orders.store_id -> stores : many_to_one order->store
	if !hasJoin(m, "order", "store") || !hasJoin(m, "order_item", "order") {
		t.Errorf("joins not derived from FKs: %+v", m.Joins)
	}
	// region (text) → categorical dim; order_date (timestamp) → time dim
	if m.Dimension("store_region") == nil || m.Dimension("order_status") == nil {
		t.Errorf("categorical dims missing")
	}
	// dimName dedups the entity prefix, so order.order_date → "order_date".
	if d := m.Dimension("order_date"); d == nil || d.Type != "time" {
		t.Errorf("time dimension not classified: %+v", d)
	}
	// quantity (numeric, non-key) → sum metric; a count metric per entity
	if m.Metric("order_item_quantity_sum") == nil || m.Metric("order_count") == nil {
		t.Errorf("metrics not generated")
	}
	// key columns must not become metrics/dims
	if m.Dimension("order_store_id") != nil {
		t.Errorf("FK column leaked into a dimension")
	}

	// And the generated model compiles to safe SQL.
	c, err := semantic.Compile(m, semantic.Query{Metrics: []string{"order_item_quantity_sum"}, GroupBy: []string{"store_region"}}, semantic.Postgres{})
	if err != nil {
		t.Fatalf("generated model does not compile: %v", err)
	}
	if c.SQL == "" {
		t.Error("empty SQL")
	}
}

// Generate with a nil AskFunc returns the heuristic baseline plus lint notes.
func TestGenerateHeuristicOnly(t *testing.T) {
	m, issues, err := Generate(context.Background(), sampleSchema(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(m.Metrics) == 0 {
		t.Fatal("no metrics")
	}
	for _, is := range issues {
		if is.Severity == "error" {
			t.Errorf("generated model has a lint error: %s", is)
		}
	}
}

func entityNames(m *semantic.Model) []string {
	var out []string
	for i := range m.Entities {
		out = append(out, m.Entities[i].Name)
	}
	return out
}

func hasJoin(m *semantic.Model, from, to string) bool {
	for _, j := range m.Joins {
		if j.From == from && j.To == to {
			return true
		}
	}
	return false
}

// The first live run of `di model gen` offered "sum of store_id" and "sum of
// sku". Both are meaningless, and both make every other generated metric look
// equally unconsidered to the customer they are shown to. The declared-key
// check misses them whenever the foreign key was never declared, which is the
// normal state of a warehouse loaded by ETL.
func TestIdentifierColumnsAreDimensionsNotMeasures(t *testing.T) {
	for _, name := range []string{"id", "store_id", "sku", "customer_uuid", "region_key", "product_code"} {
		if !looksLikeIdentifier(name) {
			t.Errorf("%q should be treated as an identifier", name)
		}
	}
	// Dropping a real measure is worse than leaving one identifier in: a
	// missing metric is invisible, a silly one is not. So the ambiguous
	// suffixes stay out of the list.
	for _, name := range []string{"amount", "qty", "unit_price", "item_num", "order_no", "cost"} {
		if looksLikeIdentifier(name) {
			t.Errorf("%q is a measure, not an identifier", name)
		}
	}
}

func TestHeuristicModelPutsUndeclaredKeysInDimensions(t *testing.T) {
	schema := &Schema{Tables: []Table{{
		Name: "orders", PrimaryKey: "order_id",
		Columns: []Column{
			{Name: "order_id", Type: "bigint"},
			{Name: "store_id", Type: "int"},   // no declared FK — the common case
			{Name: "amount", Type: "decimal"}, // a real measure
		},
	}}}
	m, err := HeuristicModel(schema)
	if err != nil {
		t.Fatal(err)
	}
	var metrics, dims []string
	for i := range m.Metrics {
		metrics = append(metrics, m.Metrics[i].Name)
	}
	for i := range m.Dimensions {
		dims = append(dims, m.Dimensions[i].Name)
	}
	for _, bad := range metrics {
		if bad == "order_store_id_sum" {
			t.Errorf("store_id became a metric: %v", metrics)
		}
	}
	if !has(metrics, "order_amount_sum") {
		t.Errorf("the real measure is missing: %v", metrics)
	}
	// It is still worth grouping by: an identifier is a dimension, not nothing.
	if !has(dims, "order_store_id") {
		t.Errorf("store_id should be a dimension: %v", dims)
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
