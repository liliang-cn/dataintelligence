package board

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const model = `
entities:
  - {name: sale, table: sales, primary_key: id}
dimensions:
  - {name: region, entity: sale, column: region, type: categorical, synonyms: [大区]}
metrics:
  - {name: revenue, description: d, synonyms: [r], entity: sale, agg: sum, expr: "amount"}
  - {name: margin,  description: d, synonyms: [m], entity: sale, agg: sum, expr: "amount - cost", roles: [finance, admin]}
`

func eng(t *testing.T) *engine.Engine {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "wh.db")
	wh, err := warehouse.OpenSQLite(context.Background(), p+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := wh.Exec(ctx, `CREATE TABLE sales (id INTEGER PRIMARY KEY, region TEXT, amount REAL, cost REAL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][3]any{{"East", 100.0, 60.0}, {"West", 250.0, 150.0}, {"East", 50.0, 20.0}} {
		if _, err := wh.Exec(ctx, `INSERT INTO sales (region, amount, cost) VALUES (?,?,?)`, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	_ = wh.Close()

	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(ctx, mp, "sqlite://"+p+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func build(t *testing.T, role string, panels ...Panel) *Board {
	t.Helper()
	b, err := Build(context.Background(), eng(t),
		governance.Principal{User: "cli", Role: role}, governance.DefaultPolicy(),
		"Board", panels)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return b
}

// Every number on the board was executed here, and the statement that produced
// it travels with it.
func TestAPanelCarriesItsRowsItsSQLAndWhereTheyCameFrom(t *testing.T) {
	b := build(t, "analyst", Panel{Title: "Revenue by region", Metrics: []string{"revenue"}, GroupBy: []string{"region"}})
	p := b.Panels[0]
	if p.Error != "" {
		t.Fatalf("panel refused: %s", p.Error)
	}
	if len(p.Rows) != 2 {
		t.Fatalf("rows = %v, want East and West", p.Rows)
	}
	if !strings.Contains(strings.ToLower(p.SQL), "select") {
		t.Errorf("no SQL travelled with the panel: %q", p.SQL)
	}
	if !strings.Contains(p.Source, "revenue") || !strings.Contains(p.Source, "model ") {
		t.Errorf("source does not name the metric and the model: %q", p.Source)
	}
	// The measure is right-aligned by declaration; the dimension is not.
	if p.Columns[0].Align != "" || p.Columns[1].Align != "right" {
		t.Errorf("alignment = %+v", p.Columns)
	}
}

// A refusal is content: one role's restriction must not blank the board.
func TestARefusedPanelLeavesTheRestOfTheBoardStanding(t *testing.T) {
	b := build(t, "analyst",
		Panel{Title: "Revenue", Metrics: []string{"revenue"}, GroupBy: []string{"region"}},
		Panel{Title: "Margin", Metrics: []string{"margin"}, GroupBy: []string{"region"}})
	if b.Panels[0].Error != "" {
		t.Errorf("the allowed panel was dropped: %s", b.Panels[0].Error)
	}
	if b.Panels[1].Error == "" {
		t.Fatal("an analyst got a metric gated to finance")
	}
	if len(b.Panels[1].Rows) != 0 {
		t.Error("a refused panel carried rows")
	}
	if !strings.Contains(b.Panels[1].Source, "margin") {
		t.Errorf("a refused panel does not say what it wanted: %q", b.Panels[1].Source)
	}
	// And finance sees it.
	f := build(t, "finance", Panel{Title: "Margin", Metrics: []string{"margin"}, GroupBy: []string{"region"}})
	if f.Panels[0].Error != "" {
		t.Errorf("finance was refused its own metric: %s", f.Panels[0].Error)
	}
}

// The fence must parse as the renderer parses it, or the whole block renders
// as nothing.
func TestTheFenceIsTheShapeTheRendererReads(t *testing.T) {
	b := build(t, "analyst", Panel{Title: "Revenue by region", Metrics: []string{"revenue"}, GroupBy: []string{"region"}})
	fence, err := b.Fence()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fence, "```dashboard\n") || !strings.HasSuffix(fence, "\n```") {
		t.Fatalf("fence is not a dashboard block:\n%s", fence)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(fence, "```dashboard\n"), "\n```")
	var back struct {
		Title  string `json:"title"`
		Panels []struct {
			Title   string         `json:"title"`
			Columns []any          `json:"columns"`
			Rows    [][]any        `json:"rows"`
			Chart   map[string]any `json:"chart"`
			SQL     string         `json:"sql"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(body), &back); err != nil {
		t.Fatalf("the fence body is not JSON: %v\n%s", err, body)
	}
	if len(back.Panels) != 1 || back.Panels[0].Title != "Revenue by region" {
		t.Fatalf("round trip lost the panel: %+v", back)
	}
	if back.Panels[0].Chart["series"] == nil {
		t.Error("a single-dimension panel drew no chart")
	}
}

// A measure must reach the chart as a number, whatever the driver handed back.
func TestATextualNumberBecomesANumberAndACodeDoesNot(t *testing.T) {
	for _, c := range []struct {
		in   any
		want any
	}{
		{"123.5", 123.5},
		{[]byte("42"), 42.0},
		{"0012", "0012"}, // a store code, not twelve
		{"2026-09-22", "2026-09-22"},
		{"East", "East"},
		{nil, nil},
		{true, true},
	} {
		if got := cell(c.in); got != c.want {
			t.Errorf("cell(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

// A chart that would mislead is not drawn. The table is still there.
func TestNoChartRatherThanAMisleadingOne(t *testing.T) {
	// No dimension: one number, nothing to plot.
	b := build(t, "analyst", Panel{Title: "Revenue", Metrics: []string{"revenue"}})
	if b.Panels[0].Chart != nil {
		t.Error("a single figure was given a chart")
	}
	if len(b.Panels[0].Rows) != 1 {
		t.Errorf("the figure itself is missing: %+v", b.Panels[0].Rows)
	}
	// Explicitly asked for none.
	n := build(t, "analyst", Panel{Title: "Revenue", Metrics: []string{"revenue"}, GroupBy: []string{"region"}, Chart: "none"})
	if n.Panels[0].Chart != nil {
		t.Error("chart: none still drew a chart")
	}
}

// The line above the numbers, for a board built from a definition nobody signed.
func TestAnUnapprovedBoardSaysSoAboveTheNumbers(t *testing.T) {
	b := build(t, "analyst", Panel{Title: "Revenue", Metrics: []string{"revenue"}, GroupBy: []string{"region"}})
	md, err := b.Markdown("abc123", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "Nobody has approved") {
		t.Errorf("an unapproved board does not say so:\n%s", md)
	}
	if strings.Index(md, "Nobody has approved") > strings.Index(md, "```dashboard") {
		t.Error("the warning is below the numbers")
	}
	signed, err := b.Markdown("abc123", "张三", "2026-09-22", "口径复核")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(signed, "张三") || strings.Contains(signed, "Nobody has approved") {
		t.Errorf("an approved board does not name its approver:\n%s", signed)
	}
}

// A COUNT arrives as int64, not float64. Asserting one Go type meant three
// panels of a twelve-panel board came back as bare tables, which reads as
// "this metric is unchartable" rather than "the host handles one type".
func TestAnIntegerMeasureIsCharted(t *testing.T) {
	for _, v := range []any{int64(3), int(3), int32(3), float32(3), uint64(3), 3.0} {
		if n, ok := asNumber(v); !ok || n != 3 {
			t.Errorf("asNumber(%T %v) = %v, %v", v, v, n, ok)
		}
	}
	if _, ok := asNumber("3"); ok {
		t.Error("a string was accepted as a plottable number")
	}
	rows := [][]any{{"East", int64(2)}, {"West", int64(1)}}
	ch := chartFor(Panel{GroupBy: []string{"region"}}, []string{"region", "order_count"}, rows)
	if ch == nil {
		t.Fatal("a counted measure drew no chart")
	}
	series := ch.(map[string]any)["series"].([]any)[0].(map[string]any)
	if got := series["data"].([]any); len(got) != 2 || got[0].(float64) != 2 {
		t.Errorf("series data = %v", got)
	}
}

// A line chart whose x-axis is out of order draws a zigzag that means nothing
// — and looks like a trend. The months came back 04, 06, 03, 02, 07, 08, 05,
// 01 and were drawn in that order.
func TestATimeSeriesIsOrderedForwards(t *testing.T) {
	rows := [][]any{{"2026-04", 4.0}, {"2026-01", 1.0}, {"2026-03", 3.0}, {"2026-02", 2.0}}
	got := order(Panel{Grain: "month"}, rows)
	for i, want := range []string{"2026-01", "2026-02", "2026-03", "2026-04"} {
		if got[i][0] != want {
			t.Fatalf("row %d = %v, want %s; full order %v", i, got[i][0], want, got)
		}
	}
}

// A broken-down panel answers "which is biggest", so it is sorted that way.
func TestABreakdownLeadsWithTheBiggest(t *testing.T) {
	rows := [][]any{{"home", 3.0}, {"kitchen", 9.0}, {"outdoor", 5.0}}
	got := order(Panel{GroupBy: []string{"category"}}, rows)
	if got[0][0] != "kitchen" || got[2][0] != "home" {
		t.Errorf("order = %v, want kitchen first and home last", got)
	}
}

// A timestamp on a month-grained axis must name the month, not a day, a clock
// and the server's timezone.
func TestAPeriodIsRenderedAtItsGrain(t *testing.T) {
	ts := time.Date(2026, 4, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for _, c := range []struct{ grain, want string }{
		{"month", "2026-04"},
		{"quarter", "2026-Q2"},
		{"year", "2026"},
		{"day", "2026-04-01"},
		{"", "2026-04-01"},
	} {
		if got := period(ts, c.grain); got != c.want {
			t.Errorf("period(%s) = %q, want %q", c.grain, got, c.want)
		}
	}
	if got := periodCell(ts, "month"); got != "2026-04" {
		t.Errorf("periodCell = %v", got)
	}
	// A driver that already returned text is left alone rather than reparsed.
	if got := periodCell("2026-04", "month"); got != "2026-04" {
		t.Errorf("a textual period was rewritten: %v", got)
	}
}
