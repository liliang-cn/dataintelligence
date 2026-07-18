package mcp

import (
	"context"
	"path/filepath"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/grounding"
)

// groundTestModel is a tiny in-memory model (no warehouse) for grounding.
func groundTestModel(t *testing.T) *semantic.Model {
	t.Helper()
	m := &semantic.Model{
		Entities: []semantic.Entity{
			{Name: "store", Table: "stores", PrimaryKey: "store_id"},
			{Name: "sale", Table: "sales", PrimaryKey: "sale_id"},
		},
		Dimensions: []semantic.Dimension{
			{Name: "store_region", Entity: "store", Column: "region", Type: "categorical"},
		},
		Metrics: []semantic.Metric{
			{Name: "revenue", Description: "total revenue", Synonyms: []string{"营收", "sales"}, Entity: "sale", Agg: "sum", Expr: "amount"},
		},
	}
	if err := m.Index(); err != nil {
		t.Fatalf("index: %v", err)
	}
	return m
}

// TestGroundTool exercises the new `ground` MCP tool end to end (retrieval +
// keyword fallback; no LLM, no warehouse) and checks it returns a typed query.
func TestGroundTool(t *testing.T) {
	ctx := context.Background()
	gr, err := grounding.New(ctx, groundTestModel(t), filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatalf("grounder: %v", err)
	}
	defer gr.Close()

	s := &srv{
		opts: &Options{
			Default:  Principal{User: "local", Role: "finance", Scopes: []string{"metrics:read"}},
			Grounder: gr,
		},
	}

	res, out, err := s.ground(ctx, nil, groundIn{Question: "revenue by store region"})
	if err != nil {
		t.Fatalf("ground: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res.Content)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output is not a map: %T", out)
	}
	metrics, _ := m["metrics"].([]string)
	if !hasString(metrics, "revenue") {
		t.Fatalf("expected revenue in metrics, got %v", metrics)
	}
	groupBy, _ := m["group_by"].([]string)
	if !hasString(groupBy, "store_region") {
		t.Fatalf("expected store_region in group_by, got %v", groupBy)
	}
}

// TestGroundToolScopeGuard: a caller lacking metrics:read is denied.
func TestGroundToolScopeGuard(t *testing.T) {
	s := &srv{opts: &Options{Default: Principal{User: "nobody", Role: "guest"}}}
	res, _, err := s.ground(context.Background(), nil, groundIn{Question: "revenue"})
	if err != nil {
		t.Fatalf("ground: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a scope-denied error result")
	}
}

// TestGroundToolNotConfigured: no grounder wired → a clear error, not a panic.
func TestGroundToolNotConfigured(t *testing.T) {
	s := &srv{opts: &Options{Default: Principal{User: "local", Role: "finance", Scopes: []string{"metrics:read"}}}}
	res, _, err := s.ground(context.Background(), nil, groundIn{Question: "revenue"})
	if err != nil {
		t.Fatalf("ground: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a not-configured error result")
	}
}

func hasString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
