package agenttools

import (
	"testing"
	"time"

	semantic "github.com/liliang-cn/semantic-go"
)

const qmodel = `
entities:
  - {name: run, table: runs, primary_key: run_id}
dimensions:
  - {name: shift, entity: run, column: shift, type: categorical}
  - {name: day,   entity: run, column: day,   type: time}
metrics:
  - {name: output, description: x, entity: run, agg: sum, expr: qty}
`

func TestParseQuery(t *testing.T) {
	m, err := semantic.Load([]byte(qmodel))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 10, 15, 0, 0, 0, time.UTC)
	q, err := ParseQuery(m, map[string]any{
		"metrics": "output", "group_by": []any{"shift", "day"}, "time_grain": "week", "order_by": "output", "desc": true, "limit": float64(5),
		"filters": []any{map[string]any{"dimension": "shift", "value": "night"}},
		"time":    map[string]any{"last_days": float64(7)},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Metrics) != 1 || q.TimeGrain != "week" || !q.Descending || q.Limit != 5 {
		t.Fatalf("q = %+v", q)
	}
	if len(q.Where) != 3 || q.Where[0].Op != "=" || q.Where[0].Values[0] != "night" {
		t.Fatalf("where = %+v", q.Where)
	}
	if q.Where[1].Dimension != "day" || q.Where[1].Values[0] != "2026-05-04" || q.Where[2].Op != "<" || q.Where[2].Values[0] != "2026-05-11" {
		t.Fatalf("window = %+v %+v", q.Where[1], q.Where[2])
	}
	if _, err := semantic.Compile(m, q, semantic.Postgres{}); err != nil {
		t.Fatalf("does not compile: %v", err)
	}
	q, err = ParseQuery(m, map[string]any{"metrics": []any{"output"}, "time": map[string]any{"from": "2026-01-01", "to": "2026-02-01"}}, now)
	if err != nil || len(q.Where) != 2 || q.Where[0].Values[0] != "2026-01-01" {
		t.Fatalf("from/to = %+v %v", q.Where, err)
	}
	if _, err := ParseQuery(m, map[string]any{}, now); err == nil {
		t.Fatal("metrics is required")
	}
}
