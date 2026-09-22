// Package board turns governed answers into an AIGUI dashboard fence.
//
// The division of labour is the whole design, and it is AIGUI's, not this
// package's: a model proposes the layout — which metrics, by which dimensions —
// and the host writes every number by running the queries itself. A model that
// can invent a panel's rows can invent the dashboard that proves its own point,
// and a dashboard is exactly the artefact people stop checking.
//
// So nothing here asks a model for a figure. Panels are compiled from the
// semantic model, executed through the governance layer as the real caller, and
// serialized with the SQL that produced them. What a model may contribute is
// the choice of panels, and even that arrives as metric and dimension names
// that must resolve against the model or the panel refuses.
//
// # A refused panel is content
//
// A caller who may not read gross margin still gets the rest of the board, with
// that one panel carrying the refusal where its numbers would have been. The
// alternative — failing the whole board — means one role's restriction blanks
// everybody's dashboard, and the first thing anyone does about that is share a
// login.
//
// # Every panel carries where its numbers came from
//
// The fence has a `sql` field shown behind a disclosure, and a `source` line.
// Both are filled: the compiled statement, and the metric names plus the hash
// of the model that defined them. A board built from a definition nobody
// approved says so in its title, for the same reason `di brief` does.
package board

import (
	"context"
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

// The fence's own limits. Exceeding one does not degrade the panel — it voids
// the whole block at parse time, and the board renders as nothing at all. So
// they are enforced here, where a too-long title can still be shortened.
const (
	maxPanels      = 12
	maxColumns     = 32
	maxRows        = 500
	maxCellLength  = 512
	maxLabelLength = 256
)

// Panel is one thing to show: a metric or three, optionally broken down.
type Panel struct {
	Title   string
	Metrics []string
	GroupBy []string
	Grain   string // day | week | month | quarter | year
	Limit   int
	// Chart is the shape to draw. Empty means the host picks: a time grain
	// draws a line, a single dimension draws a bar, anything else is a table
	// with no chart — a pie of fourteen slices is not a chart, it is a mess.
	Chart string // bar | line | pie | none
}

// Board is what gets serialized.
type Board struct {
	Title  string  `json:"title,omitempty"`
	Panels []panel `json:"panels"`
}

type panel struct {
	Title   string   `json:"title"`
	Columns []column `json:"columns,omitempty"`
	Rows    [][]any  `json:"rows,omitempty"`
	Chart   any      `json:"chart,omitempty"`
	SQL     string   `json:"sql,omitempty"`
	Note    string   `json:"note,omitempty"`
	Error   string   `json:"error,omitempty"`
	Source  string   `json:"source,omitempty"`
}

type column struct {
	Name  string `json:"name"`
	Align string `json:"align,omitempty"`
}

// Build runs every panel and returns the board.
//
// It never returns an error for a panel that failed: the failure is the
// panel's content. It returns one only when there is nothing to run at all.
func Build(ctx context.Context, eng *engine.Engine, who governance.Principal,
	pol governance.Policy, title string, panels []Panel) (*Board, error) {

	if eng == nil || !eng.Governed() {
		return nil, fmt.Errorf("board: this database has no semantic model, so there are no metrics to put on a board")
	}
	if len(panels) == 0 {
		return nil, fmt.Errorf("board: no panels")
	}
	if len(panels) > maxPanels {
		// Truncating is better than voiding: the fence rejects a thirteenth
		// panel by rejecting the whole block, and a board that renders as
		// nothing teaches the operator nothing.
		panels = panels[:maxPanels]
	}
	b := &Board{Title: clip(title, maxLabelLength)}
	for _, p := range panels {
		b.Panels = append(b.Panels, run(ctx, eng, who, pol, p))
	}
	return b, nil
}

func run(ctx context.Context, eng *engine.Engine, who governance.Principal,
	pol governance.Policy, p Panel) panel {

	out := panel{Title: clip(p.Title, maxLabelLength)}
	if len(p.Metrics) == 0 {
		out.Error = "this panel names no metric"
		return out
	}
	q := semantic.Query{Metrics: p.Metrics, GroupBy: p.GroupBy, TimeGrain: p.Grain, Limit: p.Limit}
	ans, err := governance.Query(ctx, eng, q, who, pol)
	if err != nil {
		// A refusal, a missing metric, a warehouse that is down — all of them
		// belong in the panel rather than in a stack trace, and all of them
		// read the same way to the person looking at the board: this square
		// has no numbers, and here is the sentence that says why.
		out.Error = err.Error()
		out.Source = source(p.Metrics, eng.ModelHash)
		return out
	}
	out.SQL = ans.SQL
	out.Source = source(p.Metrics, eng.ModelHash)
	out.Columns = columnsFor(ans.Columns, p.GroupBy)
	out.Rows, out.Note = rowsFor(ans.Rows, p.Grain)
	out.Rows = order(p, out.Rows)
	out.Chart = chartFor(p, ans.Columns, out.Rows)
	return out
}

// columnsFor marks the measure columns right-aligned. The fence takes the
// alignment as a declaration rather than sniffing the values, which is right:
// a formatted "9,308,286.52" is a string, and a store code that happens to be
// digits is not a number.
func columnsFor(cols []string, groupBy []string) []column {
	grouped := map[string]bool{}
	for _, g := range groupBy {
		grouped[g] = true
	}
	out := make([]column, 0, len(cols))
	for i, c := range cols {
		if i >= maxColumns {
			break
		}
		col := column{Name: clip(c, maxLabelLength)}
		if !grouped[c] {
			col.Align = "right"
		}
		out = append(out, col)
	}
	return out
}

// rowsFor caps the rows and says so rather than silently showing a prefix. A
// board that quietly drops the tail is one where somebody reads the top ten as
// the whole population.
func rowsFor(rows [][]any, grain string) ([][]any, string) {
	note := ""
	if len(rows) > maxRows {
		note = fmt.Sprintf("showing the first %d of %d rows", maxRows, len(rows))
		rows = rows[:maxRows]
	}
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		cells := make([]any, 0, len(r))
		for i, v := range r {
			if i >= maxColumns {
				break
			}
			// The first column of a grained panel is the period, and it is
			// the only place a timestamp should be rendered as a bucket.
			if i == 0 && grain != "" {
				cells = append(cells, periodCell(v, grain))
				continue
			}
			cells = append(cells, cell(v))
		}
		out = append(out, cells)
	}
	return out, note
}

// cell renders one value for a board.
//
// The fence takes string | number | boolean | null. A driver hands back
// whatever it likes — Postgres NUMERIC arrives as text — and a number that
// reaches the fence as a twenty-digit string is both ugly and, on the chart
// side, not a number at all. So numbers become numbers and everything else
// becomes a clipped string.
// periodCell renders the bucket label of a grained panel.
func periodCell(v any, grain string) any {
	if t, ok := v.(time.Time); ok {
		return period(t, grain)
	}
	// Some drivers hand a period back as text already. Parsing and
	// reformatting it would be a second guess at a format the warehouse
	// already chose, so a string is taken as it is.
	return cell(v)
}

func cell(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return t
	case []byte:
		return numeric(string(t))
	case string:
		return numeric(t)
	}
	return clip(fmt.Sprint(v), maxCellLength)
}

// numeric turns a driver's textual number back into one, and leaves anything
// else alone. A date is text, a country code is text, and "0012" is a code
// rather than twelve — so only a string that round-trips through a float and
// back to the same shape is taken as a number.
func numeric(s string) any {
	t := strings.TrimSpace(s)
	if t == "" || !looksNumeric(t) {
		return clip(s, maxCellLength)
	}
	var f float64
	if _, err := fmt.Sscanf(t, "%g", &f); err != nil {
		return clip(s, maxCellLength)
	}
	return f
}

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	// A leading zero followed by a digit is a code, not a number: "0012" is a
	// store id and rendering it as 12 loses the thing that identifies it.
	if len(s) > 1 && s[0] == '0' && s[1] != '.' {
		return false
	}
	dots := 0
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '-' && i == 0:
		case r == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func source(metrics []string, hash string) string {
	s := strings.Join(metrics, ", ")
	if hash != "" {
		s += " · model " + hash
	}
	return clip(s, maxLabelLength)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
