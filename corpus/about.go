package corpus

import (
	"context"
	"strings"

	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Finding the document about a metric, rather than the document that happens to
// use the same words.
//
// Until now the two halves of an answer were joined by nothing but the wording
// of the question. Ask "统一口径的一次合格率" and the corpus searched for those
// characters; it found the right passage on the real engagement, and it found
// it by luck — the proposal happened to spell the metric the way the asker did.
// Rephrase the question and the citation changes, which is not a property
// anybody wants in the thing that is supposed to settle disputes.
//
// The model already holds the join. A metric has a name, synonyms written down
// precisely so people can ask for it in their own words, and a description that
// is usually the definition itself. A document can be tagged with the metric it
// is about. Both were present and neither was used.
//
// So recall about a metric has two paths and they answer different questions:
//
//   - Tagged: somebody said this document is about this metric. That is an
//     assertion, not a guess, and it comes first regardless of what the words
//     look like. A definition memo tagged `一次合格率_铸造` is the answer to a
//     question about that metric even if it never repeats the phrase.
//   - Vocabulary: the metric's own name, synonyms and description are added to
//     the query, so the search runs on the model's words and not only on the
//     asker's. This is what finds the paragraph nobody remembered to tag.
//
// The relevance floor sees the metric vocabulary too. Enriching the query and
// then filtering on the bare question would have thrown away exactly the
// passages the enrichment found — a silent version of doing nothing.

// Metric is what the semantic model knows about one metric, reduced to the part
// the corpus can search on.
type Metric struct {
	Name        string
	Description string
	Synonyms    []string
}

// About is a question plus whatever the model resolved it to.
type About struct {
	Question string
	Metrics  []Metric
}

// terms is every word worth searching on: the question's and the model's.
func (a About) vocabulary() []string {
	var out []string
	for _, m := range a.Metrics {
		if m.Name != "" {
			out = append(out, m.Name)
		}
		out = append(out, m.Synonyms...)
		if m.Description != "" {
			out = append(out, m.Description)
		}
	}
	return out
}

func (a About) names() []string {
	var out []string
	for _, m := range a.Metrics {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out
}

// RecallAbout returns the passages that bear on a question, using what the
// model resolved it to as well as how it was worded.
//
// With no metrics it is exactly Recall, so a caller that could not ground the
// question loses nothing by calling this instead.
func (s *Store) RecallAbout(ctx context.Context, a About, k int) ([]Passage, error) {
	if k <= 0 {
		k = 4
	}
	seen := map[string]bool{}
	var out []Passage

	// Tagged first: an assertion beats a guess.
	for _, name := range a.names() {
		tagged, err := s.tagged(ctx, a, name, k)
		if err != nil {
			return nil, err
		}
		for _, p := range tagged {
			if seen[p.Text] {
				continue
			}
			seen[p.Text] = true
			out = append(out, p)
		}
	}

	rest, err := s.search(ctx, a, k)
	if err != nil {
		return nil, err
	}
	for _, p := range rest {
		if seen[p.Text] {
			continue
		}
		seen[p.Text] = true
		out = append(out, p)
	}
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// tagged returns the passages of documents somebody attached to this metric.
//
// The filing decides membership and the question decides order. Ranking the
// tagged passages by closeness to the metric's own name instead looked
// reasonable and was not: on the real engagement it returned the accuracy
// table from a delivery report — which mentions the metric repeatedly — ahead
// of the paragraph that defines it. Every chunk in a document filed under a
// metric is about that metric, so the metric name cannot separate them; what
// separates them is what was actually asked.
func (s *Store) tagged(ctx context.Context, a About, metric string, k int) ([]Passage, error) {
	if s.emb == nil || strings.TrimSpace(metric) == "" {
		return nil, nil
	}
	vec, err := s.emb.Embed(ctx, strings.Join(append([]string{a.Question}, a.vocabulary()...), " "))
	if err != nil {
		return nil, nil // an embedding failure here costs a ranking, not an answer
	}
	hits, err := s.db.Vector().Search(ctx, vec, core.SearchOptions{
		Collection: Collection,
		TopK:       k,
		Filter:     map[string]string{"metric": metric},
	})
	if err != nil {
		return nil, nil
	}
	out := make([]Passage, 0, len(hits))
	for _, h := range hits {
		out = append(out, Passage{
			DocumentID: h.DocID,
			Text:       trimmed(h.Content),
			Score:      h.Score,
			Metric:     h.Metadata["metric"],
			Kind:       h.Metadata["kind"],
			Tagged:     true,
		})
	}
	return out, nil
}

// search is the hybrid retrieval path, with the model's vocabulary folded into
// the plan and into the relevance floor.
func (s *Store) search(ctx context.Context, a About, k int) ([]Passage, error) {
	candidates := k * 4
	if candidates < 12 {
		candidates = 12
	}
	vocab := a.vocabulary()
	opts := cortexdb.GraphRAGQueryOptions{
		Collection:       Collection,
		TopK:             candidates,
		PerDocumentLimit: 2,
	}
	if len(vocab) > 0 {
		opts.Plan = &cortexdb.RetrievalPlan{
			Query:            a.Question,
			Keywords:         a.names(),
			AlternateQueries: vocab,
			Collection:       Collection,
		}
	}
	res, err := s.db.SearchGraphRAG(ctx, a.Question, opts)
	if err != nil {
		return nil, err
	}
	floor := append([]string{a.Question}, vocab...)
	out := make([]Passage, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		if !bearsAny(floor, c.Content) {
			continue
		}
		out = append(out, Passage{DocumentID: c.DocumentID, Text: trimmed(c.Content), Score: c.Score})
		if len(out) == k {
			break
		}
	}
	return out, nil
}
