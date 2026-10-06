package agenttools

import (
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/consult"
)

// ParseQuery turns a tool's arguments (or a JSON body of the same shape) into
// a semantic query:
//
//	{metrics, group_by, filters: [{dimension, op, values}], time: {dimension,
//	 from, to, last_days}, time_grain, order_by, desc, limit}
//
// It is forgiving about shape (a comma-separated string for a list, `value`
// for a one-element `values`) because models are, and strict about meaning: a
// time window becomes two filters on a time dimension of the model — the
// compiler, not this function, decides whether that dimension reaches the
// metric. `now` anchors last_days.
func ParseQuery(m *semantic.Model, a map[string]any, now time.Time) (semantic.Query, error) {
	q := semantic.Query{
		Metrics:   list(a["metrics"]),
		GroupBy:   list(a["group_by"]),
		TimeGrain: optString(a["time_grain"]),
		OrderBy:   optString(a["order_by"]),
	}
	if len(q.Metrics) == 0 {
		return q, fmt.Errorf("metrics is required")
	}
	if d, ok := a["desc"].(bool); ok {
		q.Descending = d
	}
	if l, ok := toInt(a["limit"]); ok && l > 0 {
		q.Limit = l
	}
	fs, err := filters(a["filters"])
	if err != nil {
		return q, err
	}
	q.Where = fs
	if t, ok := a["time"].(map[string]any); ok && len(t) > 0 {
		dim := optString(t["dimension"])
		from, to := optString(t["from"]), optString(t["to"])
		if n, ok := toInt(t["last_days"]); ok && n > 0 {
			today := consult.Day(now)
			from = today.AddDate(0, 0, -(n - 1)).Format("2006-01-02")
			to = today.AddDate(0, 0, 1).Format("2006-01-02")
		}
		if from != "" || to != "" {
			td, err := consult.TimeDimension(m, q.Metrics[0], dim, "")
			if err != nil {
				return q, err
			}
			if from != "" {
				q.Where = append(q.Where, semantic.Filter{Dimension: td, Op: ">=", Values: []any{from}})
			}
			if to != "" {
				q.Where = append(q.Where, semantic.Filter{Dimension: td, Op: "<", Values: []any{to}})
			}
		}
	}
	return q, nil
}

func filters(v any) ([]semantic.Filter, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	var out []semantic.Filter
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("filters[%d] must be an object {dimension, op, values}", i)
		}
		f := semantic.Filter{Dimension: optString(m["dimension"]), Op: strings.ToLower(strings.TrimSpace(optString(m["op"])))}
		if f.Op == "" {
			f.Op = "="
		}
		switch vs := m["values"].(type) {
		case []any:
			f.Values = vs
		case nil:
		default:
			f.Values = []any{vs}
		}
		if v, ok := m["value"]; ok && len(f.Values) == 0 && v != nil {
			f.Values = []any{v}
		}
		out = append(out, f)
	}
	return out, nil
}

func optString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func list(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		var out []string
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	}
	return 0, false
}
