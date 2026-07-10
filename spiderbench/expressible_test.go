package spiderbench

import "testing"

// selItem builds a Spider SELECT item [agg_id, val_unit] where val_unit's inner
// col_unit carries colAgg. JSON numbers decode to float64, so we mirror that.
func selItem(outerAgg, colAgg float64) []any {
	colUnit := []any{colAgg, 0.0, false}
	valUnit := []any{0.0, colUnit, nil}
	return []any{outerAgg, valUnit}
}

// baseSQL is a minimal well-formed parsed tree with the given SELECT items.
func baseSQL(items ...[]any) map[string]any {
	sel := make([]any, len(items))
	for i, it := range items {
		sel[i] = it
	}
	return map[string]any{
		"select":    []any{false, sel},
		"from":      map[string]any{"table_units": []any{[]any{"table_unit", 1.0}}},
		"where":     []any{},
		"groupBy":   []any{},
		"having":    []any{},
		"orderBy":   []any{},
		"limit":     nil,
		"intersect": nil,
		"union":     nil,
		"except":    nil,
	}
}

func TestClassify(t *testing.T) {
	// count(*): agg_id 3 → expressible.
	if got := Classify(baseSQL(selItem(3, 0))); got != Expressible {
		t.Errorf("count(*): got %q, want %q", got, Expressible)
	}
	// SELECT name: no aggregate anywhere → row-level.
	if got := Classify(baseSQL(selItem(0, 0))); got != RowLevel {
		t.Errorf("bare column: got %q, want %q", got, RowLevel)
	}
	// SELECT name, count(*): mixed but has an aggregate → expressible.
	if got := Classify(baseSQL(selItem(0, 0), selItem(3, 0))); got != Expressible {
		t.Errorf("dimension + metric: got %q, want %q", got, Expressible)
	}
	// aggregate carried on the inner col_unit rather than the outer slot.
	if got := Classify(baseSQL(selItem(0, 5))); got != Expressible {
		t.Errorf("inner-agg col_unit: got %q, want %q", got, Expressible)
	}

	// Top-N by a metric that projects only the label → topN_label_only.
	topN := baseSQL(selItem(0, 0))
	topN["orderBy"] = []any{"desc", []any{[]any{0.0, []any{3.0, 0.0, false}, nil}}}
	topN["limit"] = 1.0
	if got := Classify(topN); got != TopNLabelOnly {
		t.Errorf("top-N label only: got %q, want %q", got, TopNLabelOnly)
	}

	// Out-of-scope shapes.
	setop := baseSQL(selItem(3, 0))
	setop["intersect"] = baseSQL(selItem(3, 0))
	if got := Classify(setop); got != SetOp {
		t.Errorf("set op: got %q, want %q", got, SetOp)
	}

	having := baseSQL(selItem(3, 0))
	having["having"] = []any{[]any{false, 2.0, []any{0.0, []any{0.0, 0.0, false}, nil}, 1.0, nil}}
	if got := Classify(having); got != Having {
		t.Errorf("having: got %q, want %q", got, Having)
	}

	nestedWhere := baseSQL(selItem(3, 0))
	nestedWhere["where"] = []any{[]any{false, 2.0, []any{0.0, []any{0.0, 0.0, false}, nil}, baseSQL(selItem(3, 0)), nil}}
	if got := Classify(nestedWhere); got != NestedWhere {
		t.Errorf("nested where: got %q, want %q", got, NestedWhere)
	}

	if got := Classify(nil); got != Malformed {
		t.Errorf("nil: got %q, want %q", got, Malformed)
	}
}
