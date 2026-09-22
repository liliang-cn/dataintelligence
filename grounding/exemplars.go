package grounding

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	semantic "github.com/liliang-cn/semantic-go"
	"gopkg.in/yaml.v3"
)

// Exemplar is a labeled (question → semantic query) pair. The bank retrieves the
// most similar exemplars for a new question and injects them into the grounding
// prompt so the LLM generalizes from patterns instead of guessing. Every fixed
// miss should be promoted into the bank.
type Exemplar struct {
	Question  string   `yaml:"question"`
	Metrics   []string `yaml:"metrics"`
	GroupBy   []string `yaml:"group_by,omitempty"`
	TimeGrain string   `yaml:"time_grain,omitempty"`
}

// Query renders the exemplar's target as a semantic query.
func (e Exemplar) Query() semantic.Query {
	return semantic.Query{Metrics: e.Metrics, GroupBy: e.GroupBy, TimeGrain: e.TimeGrain}
}

// signature is the exemplar's shape — used to dedup so retrieval returns diverse
// patterns rather than three near-identical examples.
func (e Exemplar) signature() string {
	m := append([]string(nil), e.Metrics...)
	d := append([]string(nil), e.GroupBy...)
	sort.Strings(m)
	sort.Strings(d)
	return strings.Join(m, ",") + "|" + strings.Join(d, ",") + "|" + e.TimeGrain
}

// ExemplarBank is a small, embedding-backed store of exemplars.
type ExemplarBank struct {
	emb  domain.EmbedderProvider
	path string // YAML file for durable promotion (optional)

	mu    sync.RWMutex
	items []Exemplar
	vecs  [][]float64 // unit embeddings, aligned with items (nil entries if no embedder)
}

// LoadExemplars reads a YAML exemplar file and (if an embedder is configured via
// DI_EMBED_*) embeds each question. A missing file yields an empty bank.
func LoadExemplars(ctx context.Context, path string) (*ExemplarBank, error) {
	b := &ExemplarBank{path: path}
	if emb, err := embedderFromEnv(); err == nil {
		b.emb = emb
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, err
	}
	var doc struct {
		Exemplars []Exemplar `yaml:"exemplars"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	for _, ex := range doc.Exemplars {
		if err := b.add(ctx, ex); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// Len reports how many exemplars are loaded.
func (b *ExemplarBank) Len() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.items)
}

func (b *ExemplarBank) add(ctx context.Context, ex Exemplar) error {
	var vec []float64
	if b.emb != nil {
		v, err := embedUnit(ctx, b.emb, ex.Question)
		if err != nil {
			return err
		}
		vec = v
	}
	b.mu.Lock()
	b.items = append(b.items, ex)
	b.vecs = append(b.vecs, vec)
	b.mu.Unlock()
	return nil
}

// Retrieve returns up to k exemplars most similar to the question, deduped by
// shape. Dense cosine when an embedder is present; token overlap otherwise.
func (b *ExemplarBank) Retrieve(ctx context.Context, question string, k int) []Exemplar {
	if b == nil || b.Len() == 0 || k <= 0 {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	type scored struct {
		ex    Exemplar
		score float64
	}
	scoredAll := make([]scored, 0, len(b.items))

	if b.emb != nil {
		qv, err := embedUnit(ctx, b.emb, question)
		if err == nil {
			for i, ex := range b.items {
				if b.vecs[i] != nil {
					scoredAll = append(scoredAll, scored{ex, dot(qv, b.vecs[i])})
				}
			}
		}
	}
	if len(scoredAll) == 0 { // no embedder (or embed failed) → lexical overlap
		qt := tokenSet(question)
		for _, ex := range b.items {
			scoredAll = append(scoredAll, scored{ex, overlap(qt, tokenSet(ex.Question))})
		}
	}

	sort.SliceStable(scoredAll, func(i, j int) bool { return scoredAll[i].score > scoredAll[j].score })
	var out []Exemplar
	seen := map[string]bool{}
	for _, s := range scoredAll {
		if s.score <= 0 {
			continue
		}
		sig := s.ex.signature()
		if seen[sig] {
			continue // diversity: one example per shape
		}
		seen[sig] = true
		out = append(out, s.ex)
		if len(out) >= k {
			break
		}
	}
	return out
}

// Promote adds a (question → query) exemplar and, when a path is set, appends it
// to the YAML so the fix is durable ("promote every fixed miss into the bank").
func (b *ExemplarBank) Promote(ctx context.Context, q string, query semantic.Query) error {
	ex := Exemplar{Question: q, Metrics: query.Metrics, GroupBy: query.GroupBy, TimeGrain: query.TimeGrain}
	if err := b.add(ctx, ex); err != nil {
		return err
	}
	if b.path == "" {
		return nil
	}
	b.mu.RLock()
	doc := struct {
		Exemplars []Exemplar `yaml:"exemplars"`
	}{Exemplars: append([]Exemplar(nil), b.items...)}
	b.mu.RUnlock()
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(b.path, out, 0o644)
}

func tokenSet(s string) map[string]bool {
	// CJK-aware: ASCII words + CJK character bigrams (see tokensOf).
	return tokensOf(s)
}

func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n)
}
