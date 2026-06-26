package grounding

import (
	"sort"

	semantic "github.com/liliang-cn/semantic-go"
)

// Reranker re-scores retrieval candidates against the question. Where the
// retrieval stage scores the question and each metric independently (bi-encoder
// style: BM25 ⊕ dense cosine), a reranker scores the PAIR jointly — the
// cross-encoder pattern — so a candidate whose name/synonyms actually cover the
// question's words is promoted above a merely topically-near one. This is the
// precision@1 stage that decides which metric the agent commits to.
type Reranker interface {
	Rerank(question string, model *semantic.Model, cands []ScoredMetric) []ScoredMetric
}

// LexicalCrossEncoder is a dependency-free cross-encoder: it computes joint
// (question, metric) features — name-token coverage, exact synonym/name phrase
// hits, and description term overlap — and blends them with the retrieval score.
// It needs no model weights, so it reranks deterministically and offline; an
// LLM reranker can be substituted via the same interface when latency allows.
type LexicalCrossEncoder struct {
	// Weights (sum need not be 1; relative magnitude is what matters).
	WRetrieval float64
	WLexical   float64
	WOverlap   float64
}

// DefaultReranker is a sensible cross-encoder: retrieval still anchors, but a
// strong exact name/synonym match can overtake a higher-retrieval neighbor.
func DefaultReranker() Reranker {
	return LexicalCrossEncoder{WRetrieval: 0.45, WLexical: 0.35, WOverlap: 0.20}
}

func (ce LexicalCrossEncoder) Rerank(question string, model *semantic.Model, cands []ScoredMetric) []ScoredMetric {
	if len(cands) < 2 {
		return cands
	}
	q := normalize(question)
	qTokens := tokenSet(q)

	// Retrieval scores are min-maxed so the blend is on a common 0..1 scale.
	rmax, rmin := cands[0].Score, cands[0].Score
	for _, c := range cands {
		if c.Score > rmax {
			rmax = c.Score
		}
		if c.Score < rmin {
			rmin = c.Score
		}
	}
	norm := func(s float64) float64 {
		if rmax == rmin {
			return 1
		}
		return (s - rmin) / (rmax - rmin)
	}

	out := make([]ScoredMetric, len(cands))
	for i, c := range cands {
		m := model.Metric(c.Name)
		lex, overlap := 0.0, 0.0
		if m != nil {
			lex = lexicalHit(q, qTokens, m)
			overlap = jaccard(qTokens, tokenSet(normalize(m.Description)))
		}
		out[i] = ScoredMetric{
			Name:  c.Name,
			Score: ce.WRetrieval*norm(c.Score) + ce.WLexical*lex + ce.WOverlap*overlap,
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// lexicalHit is the strongest joint signal: 1.0 if a full name/synonym phrase
// appears verbatim in the question, else the best token-coverage fraction of any
// name/synonym (how much of that label the question's words account for).
func lexicalHit(q string, qTokens map[string]bool, m *semantic.Metric) float64 {
	labels := append([]string{m.Name}, m.Synonyms...)
	best := 0.0
	for _, lab := range labels {
		p := normalize(lab)
		if p == "" {
			continue
		}
		if hasPhrase(q, p) {
			return 1.0
		}
		labTokens := tokenSet(p)
		if len(labTokens) == 0 {
			continue
		}
		hit := 0
		for t := range labTokens {
			if qTokens[t] {
				hit++
			}
		}
		if frac := float64(hit) / float64(len(labTokens)); frac > best {
			best = frac
		}
	}
	return best
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
