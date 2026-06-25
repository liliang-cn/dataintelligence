// Package obs is lightweight, OTel-shaped observability: a trace is a set of
// spans (name + duration + attributes) sharing one trace_id, persisted to the
// warehouse so a wrong answer can be traced back. Latency is
// the cost proxy here; on a real warehouse you'd join engine query history for
// credits/bytes.
package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Span is one hop with its duration and attributes.
type Span struct {
	Name  string         `json:"name"`
	Ms    int64          `json:"ms"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Trace is the whole journey of one request.
type Trace struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	StartedNs int64          `json:"-"`
	TotalMs   int64          `json:"total_ms"`
	Spans     []Span         `json:"spans"`
	Attrs     map[string]any `json:"attrs,omitempty"`
	now       func() time.Time
}

// New starts a trace. id is derived from a caller-provided nanosecond clock so
// the package stays deterministic-friendly.
func New(name string, nowNs int64) *Trace {
	return &Trace{
		ID:        fmt.Sprintf("trace-%d", nowNs),
		Name:      name,
		StartedNs: nowNs,
		Attrs:     map[string]any{},
	}
}

// Add records a span.
func (t *Trace) Add(name string, ms int64, attrs map[string]any) {
	t.Spans = append(t.Spans, Span{Name: name, Ms: ms, Attrs: attrs})
}

// Finish stamps the total wall time (caller passes end-nanos) and persists.
func (t *Trace) Finish(ctx context.Context, wh *warehouse.Warehouse, endNs int64) error {
	t.TotalMs = (endNs - t.StartedNs) / 1e6
	return Save(ctx, wh, t)
}

// Save writes the trace as JSON to the append-only _traces table.
func Save(ctx context.Context, wh *warehouse.Warehouse, t *Trace) error {
	if _, err := wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _traces (
		id text PRIMARY KEY, name text, total_ms bigint, doc jsonb, at timestamptz DEFAULT now())`); err != nil {
		return err
	}
	doc, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = wh.Exec(ctx, `INSERT INTO _traces (id, name, total_ms, doc) VALUES ($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET total_ms=$3, doc=$4`, t.ID, t.Name, t.TotalMs, string(doc))
	return err
}

// List / Get for the runtime dashboard.
func List(ctx context.Context, wh *warehouse.Warehouse, limit int) ([]*Trace, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := wh.Query(ctx, `SELECT doc FROM _traces ORDER BY at DESC LIMIT `+fmt.Sprintf("%d", limit))
	if err != nil {
		return nil, err
	}
	var out []*Trace
	for _, row := range res.Rows {
		t, err := decode(row[0])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func Get(ctx context.Context, wh *warehouse.Warehouse, id string) (*Trace, error) {
	res, err := wh.Query(ctx, `SELECT doc FROM _traces WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("trace %q not found", id)
	}
	return decode(res.Rows[0][0])
}

func decode(v any) (*Trace, error) {
	var b []byte
	switch t := v.(type) {
	case []byte:
		b = t
	case string:
		b = []byte(t)
	default:
		return nil, fmt.Errorf("unexpected doc type %T", v)
	}
	var tr Trace
	return &tr, json.Unmarshal(b, &tr)
}
