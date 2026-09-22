package corpus

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"
)

// hashEmbedder is a deterministic stand-in for an embedding endpoint: the same
// text always yields the same vector and different texts almost never collide.
// It makes no claim to be semantically useful — the tests below assert on
// lexical recall, which is the half that works without a model at all — but it
// lets the store run its hybrid path in a test with no network.
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
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "corpus.db"), hashEmbedder{})
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

const memo = `# Why revenue excludes refunds

Finance and the commercial team disagreed about this for two quarters. The
decision, taken on the 14th, is that revenue is booked net of refunds, because
a refunded order was never revenue and reporting it as such inflated every
cohort chart we published. Discounts are handled the same way and always were.

The dissent is recorded: the sales team reads revenue gross, and their targets
were set against the gross number. Those targets were restated.`

const dictionary = `# Order items dictionary

order_items.qty — units on the line, always positive.
order_items.price — unit price in the order's currency at the time of sale.
order_items.discount — money taken off this line.
order_items.refund — money returned for this line after the fact.`

// The thing the warehouse half cannot do: answer why a number is defined the
// way it is, and say where the answer came from.
func TestACorpusAnswersWhyANumberIsDefinedTheWayItIs(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, Document{ID: "revenue-memo.md", Title: "Why revenue excludes refunds",
		Body: memo, Kind: "definition", Metric: "revenue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, Document{ID: "dictionary.md", Title: "Order items dictionary",
		Body: dictionary, Kind: "dictionary"}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.Recall(ctx, "why does revenue exclude refunds", 4)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the corpus found nothing for a question its documents answer")
	}
	if hits[0].DocumentID != "revenue-memo.md" {
		t.Errorf("top passage is %q, want the memo; got %+v", hits[0].DocumentID, hits)
	}
	if !strings.Contains(hits[0].Text, "net of refunds") {
		t.Errorf("the passage does not contain the decision: %q", hits[0].Text)
	}
}

// A citation names its document. Without that the passage is just text, and
// text with no source is the thing this product exists to refuse.
func TestEveryPassageNamesTheDocumentItCameFrom(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, Document{ID: "revenue-memo.md", Body: memo, Kind: "definition"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Recall(ctx, "refunds", 4)
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range hits {
		if h.DocumentID == "" {
			t.Errorf("passage %d has no source: %q", i, h.Text)
		}
	}
}

// Nothing written about it is an answer, not a failure.
func TestAQuestionNothingWasWrittenAboutReturnsNothing(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, Document{ID: "dictionary.md", Body: dictionary, Kind: "dictionary"}); err != nil {
		t.Fatal(err)
	}
	// The original version of this test only checked that the returned text did
	// not contain "forklift", which it never would — and it passed while the
	// store cited the dictionary for a question about forklifts. Asserting the
	// result is empty is the assertion that was meant.
	for _, q := range []string{"warehouse forklift maintenance schedule", "叉车维护周期"} {
		hits, err := s.Recall(ctx, q, 4)
		if err != nil {
			t.Fatalf("an unanswerable question is not an error: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("%q cited %d passage(s) from documents that do not mention it: %+v", q, len(hits), hits)
		}
	}
}

// The floor must not swallow the questions the corpus does answer.
func TestTheRelevanceFloorKeepsTheAnswersItShould(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, Document{ID: "revenue-memo.md", Body: memo, Kind: "definition"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, Document{ID: "zh-memo.md", Body: zhMemo, Kind: "definition"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"why does revenue exclude refunds",
		"revenue refunds",
		"为什么 revenue 要扣掉退款",
		"退款 口径",
	} {
		hits, err := s.Recall(ctx, q, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Errorf("%q found nothing, but the corpus answers it", q)
		}
	}
}

const zhMemo = `# 为什么 revenue 要扣掉退款

财务和商务为这件事争了两个季度。最终决定：revenue 按净额口径，扣减退款——
一笔被退掉的订单从来就不是收入。折扣一直是这么处理的。`

// Re-adding a document replaces it: a corpus rebuilt from a directory must not
// cite the same paragraph four times because the build ran four times.
func TestReAddingADocumentDoesNotDuplicateIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	d := Document{ID: "revenue-memo.md", Body: memo, Kind: "definition"}
	for i := 0; i < 3; i++ {
		if _, err := s.Add(ctx, d); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	hits, err := s.Recall(ctx, "revenue net of refunds", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, h := range hits {
		seen[h.Text]++
	}
	for text, n := range seen {
		if n > 1 {
			t.Errorf("the same passage came back %d times after three ingests: %q", n, text[:min(60, len(text))])
		}
	}
}

// An empty document is refused rather than stored: it can never be cited, and a
// corpus that accepts it reports a document count that overstates what it holds.
func TestAnEmptyDocumentIsRefused(t *testing.T) {
	s := open(t)
	if _, err := s.Add(context.Background(), Document{ID: "blank.md", Body: "  \n "}); err == nil {
		t.Fatal("an empty document was accepted")
	}
	if _, err := s.Add(context.Background(), Document{Body: memo}); err == nil {
		t.Fatal("a document with no id was accepted")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The relevance floor must not turn into a recall limit.
//
// Asking for one passage returned nothing while asking for two returned the
// right one: the store was asked for exactly k candidates, the floor rejected
// the first, and there was no second to fall back to. How many passages
// somebody wants printed is a display preference and must not decide whether
// anything is found.
func TestAskingForOnePassageFindsTheSameOneAskingForThreeDoes(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// Several documents, only one of which bears on the question — so the
	// question's best match is not guaranteed to be retrieval's first guess.
	for id, body := range map[string]string{
		"memo.md":       memo,
		"dictionary.md": dictionary,
		"unrelated1.md": "# 排班表\n\n长春一厂三班倒，早班 6 点交接，夜班 22 点交接。",
		"unrelated2.md": "# Forklift maintenance\n\nEvery 500 hours, replace the hydraulic filter.",
	} {
		if _, err := s.Add(ctx, Document{ID: id, Body: body, Kind: "doc"}); err != nil {
			t.Fatal(err)
		}
	}
	const q = "why does revenue exclude refunds"
	three, err := s.Recall(ctx, q, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(three) == 0 {
		t.Fatal("nothing found at k=3")
	}
	one, err := s.Recall(ctx, q, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("k=1 returned %d passages while k=3 returned %d", len(one), len(three))
	}
	if one[0].DocumentID != three[0].DocumentID {
		t.Errorf("k=1 gave %q, k=3 gave %q — the best passage depends on how many were asked for",
			one[0].DocumentID, three[0].DocumentID)
	}
}

// k is honoured as a ceiling, not merely as a hint to the store.
func TestRecallNeverReturnsMoreThanAsked(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for i, body := range []string{memo, zhMemo, dictionary} {
		if _, err := s.Add(ctx, Document{ID: string(rune('a'+i)) + ".md", Body: body, Kind: "doc"}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Recall(ctx, "revenue refunds 退款", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 2 {
		t.Errorf("asked for 2 passages, got %d", len(hits))
	}
}
