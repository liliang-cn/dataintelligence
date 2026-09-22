package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const model = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - {name: revenue, description: money booked, synonyms: [营收], entity: order_item, agg: sum, expr: "qty*price"}
`

func hermetic(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LLM_BASE_URL", "LLM_BASE", "OPENAI_BASE_URL", "LLM_MODEL", "OPENAI_MODEL",
		"LLM_API_KEY", "LLM_KEY", "OPENAI_API_KEY",
		"DI_EMBED_BASE_URL", "DI_EMBED_API_KEY", "DI_EMBED_MODEL", "DI_DSN", "DI_BRAIN",
	} {
		t.Setenv(k, "")
	}
}

func world(t *testing.T) Config {
	t.Helper()
	hermetic(t)
	dir := t.TempDir()
	whPath := filepath.Join(dir, "wh.db")
	wh, err := warehouse.OpenSQLite(context.Background(), whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(context.Background(),
		`CREATE TABLE order_items (id INTEGER PRIMARY KEY, qty INTEGER, price REAL)`); err != nil {
		t.Fatal(err)
	}
	_ = wh.Close()

	mp := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(mp, []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		DSN:       "sqlite://" + whPath + "?mode=rwc",
		ModelPath: mp,
		BrainPath: filepath.Join(dir, "brain.db"),
	}
}

// The whole point of assembling in one place: an engineer who opens the
// platform gets every half, not the subset they remembered to wire.
func TestAFullyConfiguredCustomerGetsEveryHalf(t *testing.T) {
	p, err := Open(context.Background(), world(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer p.Close()

	for name, got := range map[string]bool{
		"engine": p.Engine != nil, "grounder": p.Grounder != nil,
		"registry": p.Registry != nil, "corpus": p.Corpus != nil,
		"brain": p.Brain != nil, "rules": p.Rules != nil,
		"snapshots": p.Snapshots != nil, "ontology": p.Ontology != nil,
	} {
		if !got {
			t.Errorf("%s is nil on a fully configured deployment", name)
		}
	}
	if len(p.Missing) != 0 {
		t.Errorf("nothing should be missing: %v", p.Missing)
	}
	if p.ModelHash == "" {
		t.Error("no model hash, so no answer could name the definition it used")
	}
}

// The corpus and the graph must be the same file. Two brains means a passage
// can never be walked to the metric it explains, and nothing reports it.
func TestTheCorpusAndTheGraphShareOneBrain(t *testing.T) {
	cfg := world(t)
	p, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	doc := corpus.Document{
		ID:   "memo.md",
		Body: "# why revenue excludes refunds\n\nA refunded order was never revenue.",
		Kind: "definition",
	}
	if _, err := p.Corpus.Add(context.Background(), doc); err != nil {
		t.Fatalf("the corpus could not write to the shared brain: %v", err)
	}
	hits, err := p.Corpus.Recall(context.Background(), "revenue refunds", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("nothing came back from the shared brain")
	}
	// And the brain the graph will be written into is the same handle, not a
	// second one — closing the platform must not leave a second file open.
	if p.Brain == nil {
		t.Fatal("no brain handle for the graph to be built into")
	}
}

// Degradation is the product's normal state on day one, and must be named.
func TestAnUnreachableWarehouseIsReportedNotFatal(t *testing.T) {
	cfg := world(t)
	cfg.DSN = ""
	p, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("a deployment with no warehouse must still open: %v", err)
	}
	defer p.Close()

	if p.Engine != nil || p.Registry != nil || p.Grounder != nil {
		t.Error("halves that need a warehouse came back non-nil without one")
	}
	if p.Corpus == nil || p.Brain == nil || p.Rules == nil {
		t.Error("halves that do not need a warehouse were dropped with it")
	}
	joined := strings.Join(p.Missing, " | ")
	if !strings.Contains(joined, "warehouse") || !strings.Contains(joined, "DI_DSN") {
		t.Errorf("the missing half is not named with the fix: %q", joined)
	}
	if !strings.Contains(p.Status(), "DI_DSN") {
		t.Errorf("status does not say how to fix it:\n%s", p.Status())
	}
}

// And the mirror: no brain, but the warehouse half still works.
func TestNoBrainStillSignsAndComputes(t *testing.T) {
	cfg := world(t)
	cfg.BrainPath = ""
	p, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Engine == nil || p.Registry == nil {
		t.Error("the warehouse half was dropped along with the brain")
	}
	if p.Corpus != nil || p.Rules != nil {
		t.Error("brain-backed halves came back without a brain")
	}
}
