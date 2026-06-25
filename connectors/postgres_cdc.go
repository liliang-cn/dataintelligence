package connectors

import (
	"context"
	"fmt"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// PostgresCDC is a change-data-capture source. A production CDC reads the WAL via
// logical replication; this poll-based version (watch a monotonic cursor column)
// is portable and good enough to simulate streaming for the platform. It emits
// insert events for rows whose Cursor column exceeds the last seen value.
type PostgresCDC struct {
	WH        *warehouse.Warehouse
	Table     string
	CursorCol string
	Interval  time.Duration
	cursor    int64
}

// StartCursor sets the watermark to the table's current max (so Subscribe only
// emits rows added afterward — i.e. "tail -f" semantics).
func (c *PostgresCDC) StartCursor(ctx context.Context) error {
	res, err := c.WH.Query(ctx, fmt.Sprintf(`SELECT COALESCE(MAX(%s),0) FROM "%s"`, c.CursorCol, c.Table))
	if err != nil {
		return err
	}
	c.cursor = toInt64(res.Rows[0][0])
	return nil
}

// Poll returns change events since the last cursor and advances it.
func (c *PostgresCDC) Poll(ctx context.Context) ([]ChangeEvent, error) {
	res, err := c.WH.Query(ctx, fmt.Sprintf(
		`SELECT * FROM "%s" WHERE %s > $1 ORDER BY %s LIMIT 1000`, c.Table, c.CursorCol, c.CursorCol), c.cursor)
	if err != nil {
		return nil, err
	}
	ci := indexOf(res.Columns, c.CursorCol)
	var out []ChangeEvent
	for _, row := range res.Rows {
		rec := make(Record, len(res.Columns))
		for i, col := range res.Columns {
			rec[col] = fmt.Sprintf("%v", row[i])
		}
		cur := toInt64(row[ci])
		if cur > c.cursor {
			c.cursor = cur
		}
		out = append(out, ChangeEvent{Op: "insert", Cursor: cur, Record: rec})
	}
	return out, nil
}

// Subscribe polls on an interval and streams change events until ctx is done.
func (c *PostgresCDC) Subscribe(ctx context.Context) (<-chan ChangeEvent, error) {
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	ch := make(chan ChangeEvent, 64)
	go func() {
		defer close(ch)
		t := time.NewTicker(c.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				events, err := c.Poll(ctx)
				if err != nil {
					return
				}
				for _, e := range events {
					select {
					case ch <- e:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch, nil
}

// Discover returns the table's columns (types reported as text).
func (c *PostgresCDC) Discover(ctx context.Context) (SourceSchema, error) {
	res, err := c.WH.Query(ctx, fmt.Sprintf(`SELECT * FROM "%s" LIMIT 0`, c.Table))
	if err != nil {
		return SourceSchema{}, err
	}
	fields := make([]Field, len(res.Columns))
	for i, col := range res.Columns {
		fields[i] = Field{Name: col, Type: "text"}
	}
	return SourceSchema{Name: c.Table, Fields: fields}, nil
}

// Read returns the current full table as a batch.
func (c *PostgresCDC) Read(ctx context.Context) (Batch, error) {
	schema, err := c.Discover(ctx)
	if err != nil {
		return Batch{}, err
	}
	res, err := c.WH.Query(ctx, fmt.Sprintf(`SELECT * FROM "%s"`, c.Table))
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Schema: schema}
	for _, row := range res.Rows {
		rec := make(Record, len(res.Columns))
		for i, col := range res.Columns {
			rec[col] = fmt.Sprintf("%v", row[i])
		}
		batch.Rows = append(batch.Rows, rec)
	}
	return batch, nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	default:
		var n int64
		_, _ = fmt.Sscanf(fmt.Sprintf("%v", t), "%d", &n)
		return n
	}
}
