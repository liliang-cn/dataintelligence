// Package destinations is the right edge of the data plane: Sinks deliver query
// or pipeline output somewhere. Local sinks (log, JSON file, alert, warehouse
// table) are implemented; ES/Snowflake/CRM are the same interface + a client.
package destinations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// WriteResult is what a sink reports back.
type WriteResult struct {
	Sink   string `json:"sink"`
	Count  int    `json:"count"`
	Target string `json:"target,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Sink delivers tabular results to a destination.
type Sink interface {
	Name() string
	Write(ctx context.Context, cols []string, rows [][]any) (WriteResult, error)
}

// LogSink prints rows (stdout by default).
type LogSink struct{ W io.Writer }

func (s LogSink) Name() string { return "log" }
func (s LogSink) Write(_ context.Context, cols []string, rows [][]any) (WriteResult, error) {
	w := s.W
	if w == nil {
		w = os.Stdout
	}
	for _, r := range rows {
		fmt.Fprintln(w, toMap(cols, r))
	}
	return WriteResult{Sink: "log", Count: len(rows)}, nil
}

// JSONFileSink writes rows as JSON-lines to a file.
type JSONFileSink struct{ Path string }

func (s JSONFileSink) Name() string { return "json" }
func (s JSONFileSink) Write(_ context.Context, cols []string, rows [][]any) (WriteResult, error) {
	f, err := os.Create(s.Path)
	if err != nil {
		return WriteResult{}, err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(toMap(cols, r)); err != nil {
			return WriteResult{}, err
		}
	}
	return WriteResult{Sink: "json", Count: len(rows), Target: s.Path}, nil
}

// AlertSink fires when a numeric column falls outside [Min,Max]. Notify defaults
// to printing to stderr (wire Slack/email/PagerDuty here).
type AlertSink struct {
	Column   string
	Min, Max float64
	HasMin   bool
	HasMax   bool
	Notify   func(msg string)
}

func (s AlertSink) Name() string { return "alert" }
func (s AlertSink) Write(_ context.Context, cols []string, rows [][]any) (WriteResult, error) {
	idx := indexOf(cols, s.Column)
	if idx < 0 {
		return WriteResult{}, fmt.Errorf("alert: column %q not in result", s.Column)
	}
	notify := s.Notify
	if notify == nil {
		notify = func(m string) { fmt.Fprintln(os.Stderr, "ALERT:", m) }
	}
	fired := 0
	for _, r := range rows {
		v, ok := toFloat(r[idx])
		if !ok {
			continue
		}
		if (s.HasMin && v < s.Min) || (s.HasMax && v > s.Max) {
			fired++
			notify(fmt.Sprintf("%s=%v out of range in row %v", s.Column, v, toMap(cols, r)))
		}
	}
	return WriteResult{Sink: "alert", Count: fired, Note: fmt.Sprintf("%d/%d rows fired", fired, len(rows))}, nil
}

// TableSink writes rows into a warehouse table (created if needed, all TEXT).
type TableSink struct {
	WH    *warehouse.Warehouse
	Table string
}

func (s TableSink) Name() string { return "table" }
func (s TableSink) Write(ctx context.Context, cols []string, rows [][]any) (WriteResult, error) {
	var defs, q []string
	for _, c := range cols {
		defs = append(defs, `"`+c+`" TEXT`)
		q = append(q, `"`+c+`"`)
	}
	if _, err := s.WH.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (%s)`, s.Table, join(defs))); err != nil {
		return WriteResult{}, err
	}
	ph := 1
	var vals []string
	var args []any
	for _, r := range rows {
		var g []string
		for _, c := range r {
			g = append(g, "$"+strconv.Itoa(ph))
			ph++
			args = append(args, fmt.Sprintf("%v", c))
		}
		vals = append(vals, "("+join(g)+")")
	}
	if len(vals) == 0 {
		return WriteResult{Sink: "table", Count: 0, Target: s.Table}, nil
	}
	n, err := s.WH.Exec(ctx, fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES %s`, s.Table, join(q), join(vals)), args...)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Sink: "table", Count: int(n), Target: s.Table}, nil
}

func toMap(cols []string, row []any) map[string]any {
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		if i < len(row) {
			m[c] = row[i]
		}
	}
	return m
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		f, err := strconv.ParseFloat(fmt.Sprintf("%v", t), 64)
		return f, err == nil
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
