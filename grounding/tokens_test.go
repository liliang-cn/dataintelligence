package grounding

import (
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

var testModel = semantic.Model{
	Metrics: []semantic.Metric{
		{Name: "revenue", Synonyms: []string{"营收", "销售额"}, Description: "total sales amount"},
		{Name: "units_sold", Synonyms: []string{"销量", "台数"}, Description: "units sold"},
	},
}

func TestTokensOfCJKBigrams(t *testing.T) {
	toks := tokensOf("各门店大区的营收")
	for _, want := range []string{"门店", "大区", "营收"} {
		if !toks[want] {
			t.Fatalf("expected CJK bigram %q in %v", want, toks)
		}
	}
}

func TestTokensOfASCIIUnchanged(t *testing.T) {
	toks := tokensOf("Revenue by Region")
	if !toks["revenue"] || !toks["region"] {
		t.Fatalf("ascii tokens missing: %v", toks)
	}
	if toks["by"] {
		t.Fatalf("stopword leaked into tokens: %v", toks)
	}
}

func TestFtsQueryKeepsCJK(t *testing.T) {
	// A pure-Chinese question must not collapse to an empty/degenerate query.
	q := ftsQuery("各门店大区的营收")
	if !strings.Contains(q, "营收") {
		t.Fatalf("ftsQuery dropped CJK, got %q", q)
	}
}

func TestMemLexicalSurfacesByChineseSynonym(t *testing.T) {
	// A metric whose synonym is Chinese must score for a Chinese question, with
	// no embedder and regardless of the FTS engine's tokenizer.
	g := &Grounder{model: &testModel}
	scores := g.memLexical("各门店大区的营收")
	if scores["revenue"] <= 0 {
		t.Fatalf("revenue not surfaced by Chinese synonym; scores=%v", scores)
	}
}
