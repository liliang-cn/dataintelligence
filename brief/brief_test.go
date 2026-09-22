package brief

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const model = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - name: revenue
    description: money booked, net of discounts and refunds
    synonyms: [营收, net revenue]
    entity: order_item
    agg: sum
    expr: "qty*price - discount - refund"
`

const memo = `# Why revenue excludes refunds

Finance and the commercial team disagreed for two quarters. The decision is
that revenue is booked net of refunds, because a refunded order was never
revenue and reporting it as such inflated every cohort chart we published.`

type hashEmbedder struct{}

func (hashEmbedder) Dim() int { return 16 }
func (e hashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	sum := sha256.Sum256([]byte(strings.ToLower(text)))
	v := make([]float32, 16)
	for i := range v {
		v[i] = float32(int(sum[i])-128) / 128
	}
	return v, nil
}
func (e hashEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, _ := e.Embed(ctx, t)
		out[i] = v
	}
	return out, nil
}

// world is a deployment with both halves: a warehouse holding three order
// lines, a signed model, and one memo explaining the definition.
type world struct {
	opts Options
	dir  string
}

func newWorld(t *testing.T, sign bool) *world {
	t.Helper()
	// A test that reaches a live endpoint is not a test: it would pass or fail
	// on somebody else's uptime, and the keyword path it falls back to is the
	// one every deployment without an LLM key runs anyway. Clearing these makes
	// grounding deterministic and the suite hermetic.
	for _, k := range []string{
		"LLM_BASE_URL", "LLM_BASE", "OPENAI_BASE_URL", "LLM_MODEL", "OPENAI_MODEL",
		"LLM_API_KEY", "LLM_KEY", "OPENAI_API_KEY",
		"DI_EMBED_BASE_URL", "DI_EMBED_API_KEY", "DI_EMBED_MODEL",
	} {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	dir := t.TempDir()

	whPath := filepath.Join(dir, "wh.db")
	seed(t, whPath)
	dsn := "sqlite://" + whPath + "?mode=rwc"

	modelPath := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(modelPath, []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, modelPath, dsn)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	g, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		t.Fatalf("grounding: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	c, err := corpus.Open(filepath.Join(dir, "corpus.db"), hashEmbedder{})
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Add(ctx, corpus.Document{ID: "revenue-memo.md",
		Title: "Why revenue excludes refunds", Body: memo, Kind: "definition", Metric: "revenue"}); err != nil {
		t.Fatal(err)
	}

	regWH, err := warehouse.OpenSQLite(ctx, whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regWH.Close() })
	reg := rollout.New(regWH, func() string { return "2026-09-22T03:00:00Z" })
	if sign {
		if _, err := reg.Register(ctx, "v1", modelPath); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Sign(ctx, "v1", "", "张三", "口径复核：净额法"); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Promote(ctx, "v1"); err != nil {
			t.Fatal(err)
		}
	}

	return &world{dir: dir, opts: Options{
		Engine: eng, Grounder: g, Corpus: c, Registry: reg,
		Who:    governance.Principal{User: "cli", Role: "analyst"},
		Policy: governance.DefaultPolicy(),
	}}
}

func seed(t *testing.T, path string) {
	t.Helper()
	wh, err := warehouse.OpenSQLite(context.Background(), path+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer wh.Close()
	ctx := context.Background()
	if _, err := wh.Exec(ctx, `CREATE TABLE order_items (
		id INTEGER PRIMARY KEY, qty INTEGER, price REAL, discount REAL, refund REAL)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][4]float64{{2, 10, 1, 0}, {3, 20, 0, 2}, {1, 50, 5, 0}} {
		if _, err := wh.Exec(ctx,
			`INSERT INTO order_items (qty, price, discount, refund) VALUES (?,?,?,?)`,
			row[0], row[1], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole point: one question, a figure, the person who approved the
// definition behind it, and what was written about that definition.
func TestABriefCarriesTheFigureItsApproverAndTheArgument(t *testing.T) {
	w := newWorld(t, true)
	b, err := Answer(context.Background(), "net revenue", w.opts)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}

	if len(b.Rows) != 1 {
		t.Fatalf("no figure came back: %+v (%s)", b.Rows, b.NumbersBy)
	}
	// 2*10-1 + 3*20-2 + 1*50-5 = 19 + 58 + 45 = 122
	if got := toF(b.Rows[0][0]); got != 122 {
		t.Errorf("revenue = %v, want 122 (net of discounts and refunds)", got)
	}
	if b.SignedBy != "张三" || !b.Promoted {
		t.Errorf("the figure does not name its approver: signed_by=%q promoted=%v", b.SignedBy, b.Promoted)
	}
	if b.SignNote != "口径复核：净额法" {
		t.Errorf("the approver's reason is missing: %q", b.SignNote)
	}
	if len(b.Passages) == 0 || b.Passages[0].DocumentID != "revenue-memo.md" {
		t.Fatalf("the argument behind the definition is missing: %+v", b.Passages)
	}
	if want := []string{"warehouse", "corpus"}; strings.Join(b.Sources(), ",") != strings.Join(want, ",") {
		t.Errorf("sources = %v, want both halves", b.Sources())
	}

	out := b.Text()
	for _, want := range []string{"122", "张三", "revenue-memo.md", "approved by"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered brief does not mention %q:\n%s", want, out)
		}
	}
}

// A figure from a model nobody signed says so, loudly, next to the figure.
func TestAnUnapprovedDefinitionIsNamedBesideItsNumber(t *testing.T) {
	w := newWorld(t, false)
	b, err := Answer(context.Background(), "net revenue", w.opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 1 {
		t.Fatalf("no figure: %s", b.NumbersBy)
	}
	if b.Attested || b.SignedBy != "" {
		t.Errorf("an unregistered model was reported as approved: %+v", b)
	}
	if !strings.Contains(b.Text(), "NOBODY has approved") {
		t.Errorf("the brief does not warn that nothing approved this figure:\n%s", b.Text())
	}
}

// Half a question is still answered. "Why" has no metric behind it, and the
// brief returns the argument rather than an empty table.
func TestAQuestionWithNoMetricStillReturnsWhatWasWrittenAboutIt(t *testing.T) {
	w := newWorld(t, true)
	b, err := Answer(context.Background(), "why does revenue exclude refunds", w.opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Passages) == 0 {
		t.Fatal("the corpus half was dropped because the warehouse half was thin")
	}
	if b.Passages[0].DocumentID != "revenue-memo.md" {
		t.Errorf("wrong passage: %+v", b.Passages[0])
	}
}

// A question the model cannot ground says what the grounder said, not a label
// this package made up. "the question is ambiguous: revenue" was printed for a
// question about forklift maintenance, and neither half of that sentence was
// true.
func TestAnUngroundableQuestionQuotesTheGrounderRatherThanCallingItAmbiguous(t *testing.T) {
	w := newWorld(t, true)
	b, err := Answer(context.Background(), "叉车维护周期", w.opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 0 {
		t.Fatalf("a question with no metric behind it produced a figure: %+v", b.Rows)
	}
	if strings.Contains(b.NumbersBy, "ambiguous") {
		t.Errorf("a single-candidate clarification is reported as an ambiguity: %q", b.NumbersBy)
	}
	if b.NumbersBy == "" {
		t.Error("the brief does not say why there is no figure")
	}
	// And it must not quietly become a citation either.
	if len(b.Passages) != 0 {
		t.Errorf("unrelated passages were cited: %+v", b.Passages)
	}
	if len(b.Sources()) != 0 {
		t.Errorf("sources = %v, want neither half", b.Sources())
	}
}

// A deployment with no corpus is the common one, and must still work.
func TestNoCorpusIsAConfigurationNotAnError(t *testing.T) {
	w := newWorld(t, true)
	w.opts.Corpus = nil
	b, err := Answer(context.Background(), "net revenue", w.opts)
	if err != nil {
		t.Fatalf("a warehouse-only deployment could not produce a brief: %v", err)
	}
	if len(b.Rows) != 1 {
		t.Fatalf("no figure: %s", b.NumbersBy)
	}
	if len(b.Sources()) != 1 || b.Sources()[0] != "warehouse" {
		t.Errorf("sources = %v, want warehouse only", b.Sources())
	}
}

func toF(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	}
	return -1
}

// A ratio printed to twenty decimal places is not more accurate, it is less
// readable — and that is what Postgres NUMERIC does when nothing formats it.
func TestNumbersAreRenderedForAPersonNotForADriver(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{"0.97094126231302444363", "0.970941"}, // Postgres NUMERIC, as text
		{0.97094126231302444363, "0.970941"},
		{float64(122), "122"},        // not 122.000000
		{int64(400000), "400000"},    // counts are left alone
		{"2026-09-22", "2026-09-22"}, // a date is not a number to round
		{"长春一厂", "长春一厂"},             // and neither is a label
		{"3.14", "3.14"},             // already short enough
		{nil, "<nil>"},
	} {
		if got := cell(c.in); got != c.want {
			t.Errorf("cell(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}
