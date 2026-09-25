package board

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// generated is the shape modelgen emits: a description and synonyms on every
// metric, which is why "has a description" could not tell them apart.
const rankModel = `
entities:
  - {name: batch, table: batch, primary_key: id}
dimensions:
  - {name: line_name, entity: batch, column: line, type: categorical}
metrics:
  - name: batch_count
    description: Number of distinct batch (auto-generated; review).
    synonyms: [batch count]
    entity: batch
    agg: count_distinct
    expr: id
  - name: batch_qty_sum
    description: Sum of batch.qty (auto-generated; confirm it is additive).
    synonyms: [batch qty]
    entity: batch
    agg: sum
    expr: qty
  - name: 一次合格率
    description: 清理后合格件数 ÷ 浇注件数
    synonyms: [合格率]
    entity: batch
    agg: sum
    expr: good
`

func rankWorld(t *testing.T) *engine.Engine {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	whPath := filepath.Join(dir, "wh.db")

	wh, err := warehouse.OpenSQLite(ctx, whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE batch (id INTEGER PRIMARY KEY, qty INTEGER, good INTEGER, line TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE _audit (ts TEXT, "user" TEXT, role TEXT, metrics TEXT,
		group_by TEXT, "sql" TEXT, refused INTEGER, note TEXT, question TEXT, engagement TEXT, model_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = wh.Close()

	modelPath := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(modelPath, []byte(rankModel), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, modelPath, "sqlite://"+whPath+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func ask(t *testing.T, eng *engine.Engine, metric string, n int, wasRefused bool) {
	t.Helper()
	r := 0
	if wasRefused {
		r = 1
	}
	for i := 0; i < n; i++ {
		if _, err := eng.WH.Exec(context.Background(),
			`INSERT INTO _audit (ts, metrics, refused) VALUES (?,?,?)`,
			"2026-09-22T06:00:00Z", "["+metric+"]", r); err != nil {
			t.Fatal(err)
		}
	}
}

// The board leads with what this deployment's people asked for, whatever the
// metric is called and wherever it sits in the file.
func TestTheBoardLeadsWithWhatPeopleActuallyAskedFor(t *testing.T) {
	eng := rankWorld(t)
	ask(t, eng, "一次合格率", 8, false)
	ask(t, eng, "batch_qty_sum", 2, false)

	panels := ProposeFor(context.Background(), eng, eng.Model)
	if len(panels) == 0 {
		t.Fatal("no panels")
	}
	if panels[0].Title != "一次合格率" {
		t.Fatalf("the board opens with %q; the trail says 一次合格率 was asked for most", panels[0].Title)
	}
	// And the second-most-asked outranks the one nobody asked for.
	var order []string
	for _, p := range panels {
		order = append(order, strings.SplitN(p.Title, " by ", 2)[0])
	}
	if pos(order, "batch_qty_sum") > pos(order, "batch_count") {
		t.Errorf("a metric nobody asked for outranks one people did: %v", order)
	}
}

// A refused question is not evidence that anybody may see the metric.
func TestARefusedQuestionDoesNotPromoteAMetric(t *testing.T) {
	eng := rankWorld(t)
	ask(t, eng, "batch_qty_sum", 20, true) // refused twenty times
	ask(t, eng, "一次合格率", 1, false)

	panels := ProposeFor(context.Background(), eng, eng.Model)
	if panels[0].Title != "一次合格率" {
		t.Fatalf("the board opens with %q — twenty refusals were counted as demand", panels[0].Title)
	}
}

// With no trail, the fallback must still not lead with a generated metric.
// "Has a description" cannot tell them apart: the generator writes one.
func TestWithNoTrailAHandWrittenMetricStillLeads(t *testing.T) {
	eng := rankWorld(t)
	panels := ProposeFor(context.Background(), eng, eng.Model)
	if panels[0].Title != "一次合格率" {
		t.Fatalf("with no trail the board opens with %q, a generated metric", panels[0].Title)
	}
	if handWritten(&eng.Model.Metrics[0]) {
		t.Error("batch_count is generated and was read as hand-written")
	}
	if !handWritten(&semantic.Metric{Name: "一次合格率", Description: "清理后合格件数 ÷ 浇注件数"}) {
		t.Error("a metric a person wrote was read as generated")
	}
}

// The trail read must survive whatever the engine calls a boolean. Writing the
// filter as SQL failed outright on Postgres and silently ranked nothing.
func TestTheTrailIsReadWhateverTheEngineCallsABoolean(t *testing.T) {
	for _, v := range []any{true, int64(1), 1, "true", "1", "t", []byte("true")} {
		if !wasRefused(v) {
			t.Errorf("wasRefused(%#v) = false, want true", v)
		}
	}
	for _, v := range []any{false, int64(0), 0, "false", "0", "f", []byte("false"), nil} {
		if wasRefused(v) {
			t.Errorf("wasRefused(%#v) = true, want false", v)
		}
	}
}

// The trail's metric column is a Go slice printed with %v.
func TestTheTrailsMetricColumnIsParsed(t *testing.T) {
	got := splitMetricList("[一次合格率_铸造 batch_count]")
	if len(got) != 2 || got[0] != "一次合格率_铸造" || got[1] != "batch_count" {
		t.Fatalf("parsed %v", got)
	}
	if n := len(splitMetricList("[]")); n != 0 {
		t.Errorf("an empty list parsed to %d names", n)
	}
}

func pos(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return len(list)
}

// A layout survives the round trip through a URL.
func TestALayoutSurvivesTheRoundTripThroughALink(t *testing.T) {
	want := []Panel{
		{Title: "三个口径", Metrics: []string{"一次合格率_铸造", "一次合格率_锻造"}},
		{Title: "吨件电耗", Metrics: []string{"吨件电耗"}, GroupBy: []string{"workshop_name"}, Grain: "month", Limit: 20, Chart: "bar"},
	}
	enc, err := EncodeLayout(want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "+/=") {
		t.Errorf("the link is not URL-safe: %q", enc)
	}
	got, err := DecodeLayout(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d panels, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Title != want[i].Title || strings.Join(got[i].Metrics, ",") != strings.Join(want[i].Metrics, ",") ||
			strings.Join(got[i].GroupBy, ",") != strings.Join(want[i].GroupBy, ",") ||
			got[i].Grain != want[i].Grain || got[i].Limit != want[i].Limit || got[i].Chart != want[i].Chart {
			t.Errorf("panel %d came back as %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A damaged or hostile link is refused with a reason, never rendered.
func TestADamagedLayoutLinkIsRefused(t *testing.T) {
	for name, enc := range map[string]string{
		"not base64":  "!!!!",
		"not deflate": "aGVsbG8sIHdvcmxk",
		"empty":       "",
		"no panels":   mustEncodeRaw(t, "[]"),
		"metricless":  mustEncodeRaw(t, `[{"Title":"x"}]`),
	} {
		if _, err := DecodeLayout(enc); err == nil {
			t.Errorf("%s: was accepted", name)
		}
	}
}

// A short link must not expand into an afternoon of queries.
//
// The bomb is crafted rather than produced by EncodeLayout: somebody sending a
// hostile link is not going to use our encoder, and 20,000 identical panels as
// raw JSON deflate to a couple of kilobytes.
func TestAZipBombLayoutIsRefusedRatherThanRun(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("[")
	for i := 0; i < 20000; i++ {
		if i > 0 {
			raw.WriteString(",")
		}
		raw.WriteString(`{"Title":"p","Metrics":["revenue"]}`)
	}
	raw.WriteString("]")

	enc := mustEncodeRaw(t, raw.String())
	if len(enc) > 8192 {
		t.Fatalf("the bomb is %d bytes; it should compress much further", len(enc))
	}
	if _, err := DecodeLayout(enc); err == nil {
		t.Fatalf("a %d-byte link expanded into %d bytes of panels and was accepted", len(enc), raw.Len())
	} else if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

func mustEncodeRaw(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = zw.Write([]byte(raw))
	_ = zw.Close()
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// An untitled panel is headed by what it shows, whichever path built it.
func TestAPanelWithNoTitleIsHeadedByWhatItShows(t *testing.T) {
	for _, c := range []struct {
		p    Panel
		want string
	}{
		{Panel{Metrics: []string{"revenue"}}, "revenue"},
		{Panel{Metrics: []string{"revenue", "units"}, GroupBy: []string{"region"}}, "revenue, units by region"},
		{Panel{Metrics: []string{"revenue"}, GroupBy: []string{"order_date"}, Grain: "month"}, "revenue by order_date per month"},
		{Panel{Title: "  净收入  ", Metrics: []string{"revenue"}}, "净收入"},
	} {
		if got := titleOf(c.p); got != c.want {
			t.Errorf("titleOf(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// Every panel the proposal offers must compile.
//
// On Meridian the proposal used to offer five panels the compiler refused —
// four window metrics broken down by brand, and a ratio across a one-to-many
// join — and the board printed five compile errors under a heading that
// blamed the reader's role. The proposal now asks the compiler first; this
// holds it to that against the real model.
func TestEveryProposedPanelCompilesOnTheRealModel(t *testing.T) {
	m, err := semantic.LoadFile("../models/meridian.yaml")
	if err != nil {
		t.Fatal(err)
	}
	panels := Propose(m)
	if len(panels) == 0 {
		t.Fatal("nothing proposed")
	}
	windows := 0
	for _, p := range panels {
		q := semantic.Query{Metrics: p.Metrics, GroupBy: p.GroupBy, TimeGrain: p.Grain, Roles: allRoles(m)}
		if _, err := semantic.Compile(m, q, semantic.ANSI{}); err != nil {
			t.Errorf("proposed %q, which does not compile: %v", p.Title, err)
		}
		if mt := m.Metric(p.Metrics[0]); mt != nil && mt.Window != "" {
			windows++
			if p.Grain == "" {
				t.Errorf("window metric %s proposed without a time grain", p.Title)
			}
		}
	}
	if windows == 0 {
		t.Error("Meridian's window metrics were dropped instead of shown over time")
	}
}
