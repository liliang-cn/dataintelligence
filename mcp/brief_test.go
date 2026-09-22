package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const briefModel = `
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

const briefMemo = `# Why revenue excludes refunds

The decision is that revenue is booked net of refunds, because a refunded
order was never revenue and reporting it as such inflated every cohort chart.`

// briefServer is a deployment with all three parts: a warehouse with rows, a
// signed model, and a document explaining the definition.
func briefServer(t *testing.T) *srv {
	t.Helper()
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
	wh, err := warehouse.OpenSQLite(ctx, whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE order_items (
		id INTEGER PRIMARY KEY, qty INTEGER, price REAL, discount REAL, refund REAL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][4]float64{{2, 10, 1, 0}, {3, 20, 0, 2}, {1, 50, 5, 0}} {
		if _, err := wh.Exec(ctx, `INSERT INTO order_items (qty, price, discount, refund) VALUES (?,?,?,?)`,
			r[0], r[1], r[2], r[3]); err != nil {
			t.Fatal(err)
		}
	}
	_ = wh.Close()

	modelPath := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(modelPath, []byte(briefModel), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, modelPath, "sqlite://"+whPath+"?mode=rwc")
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	gr, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gr.Close() })

	store, err := corpus.Open(filepath.Join(dir, "corpus.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Add(ctx, corpus.Document{ID: "revenue-memo.md",
		Body: briefMemo, Kind: "definition", Metric: "revenue"}); err != nil {
		t.Fatal(err)
	}

	reg := rollout.New(eng.WH, func() string { return "2026-09-22T04:00:00Z" })
	if _, err := reg.Register(ctx, "v1", modelPath); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Sign(ctx, "v1", "", "张三", "口径复核：净额法"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}

	return &srv{eng: eng, opts: &Options{
		Default:  Principal{User: "local", Role: "analyst", Scopes: []string{"metrics:read"}},
		Grounder: gr, Corpus: store, Registry: reg,
	}}
}

// An agent calling brief gets the figure and, in the same payload, everything
// it needs to say whether the figure can be trusted.
func TestBriefToolReturnsTheFigureWithItsProvenance(t *testing.T) {
	s := briefServer(t)
	res, out, err := s.brief(context.Background(), nil, briefIn{Question: "net revenue"})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	if res.IsError {
		t.Fatalf("error result: %+v", res.Content)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output is not a map: %T", out)
	}
	rows, _ := m["rows"].([][]any)
	if len(rows) != 1 {
		t.Fatalf("no figure in the payload: %+v (%v)", m["rows"], m["no_figure_because"])
	}
	if m["signed_by"] != "张三" {
		t.Errorf("signed_by = %v, want 张三", m["signed_by"])
	}
	if m["approved"] != true {
		t.Errorf("approved = %v, want true", m["approved"])
	}
	if m["reason"] != "口径复核：净额法" {
		t.Errorf("reason = %v", m["reason"])
	}
	passages, _ := m["passages"].([]map[string]any)
	if len(passages) == 0 || passages[0]["document"] != "revenue-memo.md" {
		t.Fatalf("no supporting passage: %+v", passages)
	}
	// The text the model reads must carry the provenance too, not only the
	// structured payload — an agent that reads the prose still has to be able
	// to repeat who approved this.
	text := resultText(res)
	for _, want := range []string{"122", "张三", "revenue-memo.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("the tool's text does not mention %q:\n%s", want, text)
		}
	}
}

// brief runs as the caller, like query_metric: a principal without
// metrics:read gets nothing.
func TestBriefToolIsScopeGuarded(t *testing.T) {
	s := briefServer(t)
	s.opts.Default = Principal{User: "nobody", Role: "analyst", Scopes: []string{}}
	res, _, err := s.brief(context.Background(), nil, briefIn{Question: "net revenue"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a caller with no scope got an answer")
	}
	if !strings.Contains(resultText(res), "metrics:read") {
		t.Errorf("the refusal does not name the missing scope: %s", resultText(res))
	}
}

// A deployment with neither corpus nor registry still answers, and says plainly
// that nothing stands behind the number.
func TestBriefToolWithoutCorpusOrRegistryStillAnswers(t *testing.T) {
	s := briefServer(t)
	s.opts.Corpus, s.opts.Registry = nil, nil
	res, out, err := s.brief(context.Background(), nil, briefIn{Question: "net revenue"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("error result: %+v", res.Content)
	}
	m := out.(map[string]any)
	if rows, _ := m["rows"].([][]any); len(rows) != 1 {
		t.Fatalf("no figure: %v", m["no_figure_because"])
	}
	if m["approved"] != false || m["signed_by"] != "" {
		t.Errorf("a deployment with no registry claimed approval: %+v", m)
	}
	if !strings.Contains(resultText(res), "NOBODY has approved") {
		t.Errorf("the text does not warn the figure is unbacked:\n%s", resultText(res))
	}
}

// resultText is the text an agent would actually read out of a tool result.
func resultText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// The banner an operator reads, and the tools an agent can call, must be the
// same list. They were not: `brief` was served while the startup line still
// named seven tools.
func TestTheAdvertisedToolsAreTheServedTools(t *testing.T) {
	ctx := context.Background()
	s := briefServer(t)
	server := NewServer(s.eng, s.opts)

	ct, st := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer ss.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	served := map[string]bool{}
	for _, tool := range res.Tools {
		served[tool.Name] = true
	}
	for _, name := range ToolNames {
		if !served[name] {
			t.Errorf("ToolNames advertises %q but the server does not serve it", name)
		}
		delete(served, name)
	}
	for name := range served {
		t.Errorf("the server serves %q but ToolNames does not advertise it", name)
	}
}
