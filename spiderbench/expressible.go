package spiderbench

// Category labels how a Spider gold query relates to the semantic layer's
// expressive power (metric × dimension × filter + order/limit + time grain).
type Category string

const (
	// Expressible: a pure group-by aggregation — at least one aggregate in the
	// SELECT list (the metric appears in the output), grouped by zero or more
	// dimensions, filtered by simple predicates. This is exactly what the
	// compiler is built for.
	Expressible Category = "expressible"
	// TopNLabelOnly: intent is expressible (top-N by a metric) but the gold
	// output projects only the label, not the metric — so result shapes differ.
	// Counted separately: in scope conceptually, borderline for exec-accuracy.
	TopNLabelOnly Category = "topN_label_only"
	// RowLevel: selects individual rows/columns with no aggregate — a metric
	// layer deliberately does not answer these.
	RowLevel Category = "rowlevel"
	// out-of-scope shapes the semantic query language cannot represent:
	NestedWhere Category = "nested_where"
	NestedFrom  Category = "nested_from"
	SetOp       Category = "setop"
	Having      Category = "having"
	Malformed   Category = "malformed"
)

// InScope reports whether the question's intent is representable as a semantic
// query (the coverage numerator). Strict coverage counts Expressible only;
// lenient coverage also counts TopNLabelOnly.
func (c Category) InScope() bool { return c == Expressible || c == TopNLabelOnly }

// Classify decides a gold query's category straight from Spider's parsed tree.
// It mirrors the validated reference classifier; unexpected shapes fall through
// to Malformed rather than panicking.
func Classify(sql map[string]any) Category {
	if sql == nil {
		return Malformed
	}
	// Set operations chain a second query — not expressible.
	if isSubquery(sql["intersect"]) || isSubquery(sql["union"]) || isSubquery(sql["except"]) {
		return SetOp
	}
	// A sub-query in the FROM clause (derived table).
	from, _ := sql["from"].(map[string]any)
	if from != nil {
		if tus, ok := from["table_units"].([]any); ok {
			for _, tu := range tus {
				if a, ok := tu.([]any); ok && len(a) > 0 {
					if kind, _ := a[0].(string); kind == "sql" {
						return NestedFrom
					}
				}
			}
		}
	}
	// A sub-query nested inside a WHERE predicate.
	if condsHaveNested(sql["where"]) {
		return NestedWhere
	}
	// HAVING is not modeled by the compiler.
	if h, ok := sql["having"].([]any); ok && len(h) > 0 {
		return Having
	}
	// The SELECT list must carry at least one aggregate (the metric).
	sel := selectItems(sql)
	if len(sel) == 0 {
		return Malformed
	}
	aggs := 0
	for _, it := range sel {
		if selectItemAgg(it) != 0 {
			aggs++
		}
	}
	if aggs == 0 {
		// No metric in the output. Top-N-by-metric (ORDER BY agg + LIMIT) is
		// intent-expressible; anything else is a row-level select.
		if orderByHasAgg(sql["orderBy"]) && sql["limit"] != nil {
			return TopNLabelOnly
		}
		return RowLevel
	}
	return Expressible
}

// --- Spider parsed-tree accessors ---

// isSubquery is true when a set-op / from slot holds a nested sql object.
func isSubquery(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// selectItems returns the SELECT column list: sql["select"] = [distinct, items].
func selectItems(sql map[string]any) []any {
	s, ok := sql["select"].([]any)
	if !ok || len(s) < 2 {
		return nil
	}
	items, _ := s[1].([]any)
	return items
}

// selectItemAgg returns the aggregate id of one SELECT item, checking the outer
// agg_id and, when absent, the inner col_unit's agg_id. 0 means "no aggregate".
// item = [agg_id, val_unit]; val_unit = [unit_op, col_unit, col_unit]; col_unit = [agg_id, ...].
func selectItemAgg(item any) float64 {
	a, ok := item.([]any)
	if !ok || len(a) < 2 {
		return 0
	}
	if outer := num(a[0]); outer != 0 {
		return outer
	}
	return valUnitAgg(a[1])
}

// valUnitAgg reads the aggregate id from a val_unit's first col_unit.
func valUnitAgg(vu any) float64 {
	a, ok := vu.([]any)
	if !ok || len(a) < 2 {
		return 0
	}
	cu, ok := a[1].([]any)
	if !ok || len(cu) == 0 {
		return 0
	}
	return num(cu[0])
}

// orderByHasAgg is true when ORDER BY sorts on an aggregate (top-N by a metric).
// orderBy = [] | [dir, [val_unit, ...]].
func orderByHasAgg(v any) bool {
	ob, ok := v.([]any)
	if !ok || len(ob) < 2 {
		return false
	}
	vus, ok := ob[1].([]any)
	if !ok {
		return false
	}
	for _, vu := range vus {
		if valUnitAgg(vu) != 0 {
			return true
		}
	}
	return false
}

// condsHaveNested is true when any WHERE predicate compares against a sub-query.
// where = [cond | "and" | "or", ...]; cond = [not, op, val_unit, val1, val2].
func condsHaveNested(v any) bool {
	conds, ok := v.([]any)
	if !ok {
		return false
	}
	for _, c := range conds {
		cond, ok := c.([]any)
		if !ok || len(cond) < 5 { // skip "and"/"or" connectors
			continue
		}
		if isSubquery(cond[3]) || isSubquery(cond[4]) {
			return true
		}
	}
	return false
}

// num coerces a JSON number (float64) to float64; anything else is 0.
func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
