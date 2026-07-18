package ingest

import (
	"testing"

	"github.com/liliang-cn/dataintelligence/connectors"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"门店", "门店"},
		{"销售额", "销售额"},
		{"Order ID", "order_id"},
		{"金额(元)", "金额_元"},
		{"  Total  ", "total"},
	}
	for _, c := range cases {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInferMapping_CJKZeroConfig(t *testing.T) {
	schema := connectors.SourceSchema{
		Name: "门店销售",
		Fields: []connectors.Field{
			{Name: "门店", Type: "text"},
			{Name: "品类", Type: "text"},
			{Name: "销售额", Type: "numeric"},
		},
	}
	plan := InferMapping(schema, "some_table", nil)
	if len(plan.Fields) != len(schema.Fields) {
		t.Fatalf("got %d field maps, want %d", len(plan.Fields), len(schema.Fields))
	}
	seen := map[string]bool{}
	for _, fm := range plan.Fields {
		if fm.Target == "" {
			t.Errorf("field %q normalized to empty target", fm.Source)
		}
		if fm.Target != fm.Source {
			t.Errorf("field %q normalized to %q, want the Chinese name unchanged", fm.Source, fm.Target)
		}
		if seen[fm.Target] {
			t.Errorf("duplicate target %q", fm.Target)
		}
		seen[fm.Target] = true
	}
}
