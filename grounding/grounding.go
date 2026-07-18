// Package grounding is the context-engineering core: it indexes
// metric metadata in cortexdb, retrieves the top-K relevant metrics for a
// question (so the LLM sees only those, not the whole catalog), then asks the
// agent-go LLM to emit a semantic query — or a clarification when ambiguous.
//
// Layering: retrieval = cortexdb · LLM = agent-go · model = semantic-go.
package grounding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/llm"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	semantic "github.com/liliang-cn/semantic-go"
)

// Grounder turns NL into a semantic query, grounded on retrieved metrics.
type Grounder struct {
	model *semantic.Model
	cdb   *cortexdb.DB
	llm   *llm.Service // nil → deterministic keyword fallback
	topK  int

	emb      domain.EmbedderProvider // nil → lexical-only retrieval
	mvecs    map[string][]float64    // metric name → unit embedding (dense index)
	bank     *ExemplarBank           // few-shot exemplar bank (nil → no exemplars)
	reranker Reranker                // cross-encoder rerank stage (nil → none)
}

// Clarify is returned instead of a query when the question is ambiguous.
type Clarify struct {
	Question   string
	Candidates []string
}

// ScoredMetric is one retrieval hit.
type ScoredMetric struct {
	Name  string
	Score float64
}

// New opens a cortexdb index, indexes the model's metrics, and (if LLM_* env is
// set) wires the agent-go LLM. dbPath is a sqlite file path for the index.
func New(ctx context.Context, model *semantic.Model, dbPath string) (*Grounder, error) {
	cfg := cortexdb.DefaultConfig(dbPath)
	cfg.Dimensions = 1 // lexical (FTS/BM25) retrieval; no embedder needed
	db, err := cortexdb.Open(cfg)
	if err != nil {
		return nil, err
	}
	g := &Grounder{model: model, cdb: db, topK: 8, reranker: DefaultReranker()}
	if svc, err := llm.NewOpenAIFromEnv(); err == nil {
		g.llm = svc
	}
	if emb, err := embedderFromEnv(); err == nil && emb != nil {
		g.emb = emb
	}
	if err := g.index(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return g, nil
}

// WithExemplars attaches a few-shot exemplar bank:
// retrieved (question → semantic query) examples are injected into the grounding
// prompt so the LLM generalizes from patterns. Safe to call with a nil bank.
func (g *Grounder) WithExemplars(b *ExemplarBank) *Grounder { g.bank = b; return g }

func (g *Grounder) Close() error { return g.cdb.Close() }

// Mode reports the grounding mode for the receipt.
func (g *Grounder) Mode() string {
	retr := "bm25"
	if g.emb != nil {
		retr = "hybrid(bm25+embedding)"
	}
	if g.reranker != nil {
		retr += "+rerank"
	}
	gen := "keyword"
	if g.llm != nil {
		gen = "llm"
	}
	ex := ""
	if g.bank != nil && g.bank.Len() > 0 {
		ex = fmt.Sprintf(" + few-shot(%d)", g.bank.Len())
	}
	return retr + " + " + gen + ex
}

// index loads each metric's metadata (name + description + synonyms) as a
// searchable document. Synonyms are load-bearing: "top line" must find revenue.
func (g *Grounder) index(ctx context.Context) error {
	docs := make([]string, len(g.model.Metrics))
	for i := range g.model.Metrics {
		m := &g.model.Metrics[i]
		content := m.Name + ". " + m.Description + ". synonyms: " + strings.Join(m.Synonyms, ", ")
		docs[i] = content
		err := g.cdb.Vector().Upsert(ctx, &core.Embedding{
			ID:       m.Name,
			Vector:   []float32{0},
			Content:  content,
			Metadata: map[string]string{"name": m.Name},
		})
		if err != nil {
			return fmt.Errorf("index metric %q: %w", m.Name, err)
		}
	}
	// Dense index: embed each metric document once (one batch call).
	if g.emb != nil {
		vecs, err := g.emb.EmbedBatch(ctx, docs)
		if err != nil {
			return fmt.Errorf("embed metric docs: %w", err)
		}
		g.mvecs = make(map[string][]float64, len(vecs))
		for i := range g.model.Metrics {
			if i < len(vecs) {
				g.mvecs[g.model.Metrics[i].Name] = unit(vecs[i])
			}
		}
	}
	return nil
}

// Retrieve returns the top-K metrics for a question. With an embedder wired it
// fuses lexical (BM25) and dense (cosine) signals — hybrid retrieval; otherwise
// it is BM25-only.
func (g *Grounder) Retrieve(ctx context.Context, question string) ([]ScoredMetric, error) {
	// Lexical signal: cortexdb BM25 over the metric documents.
	hits, err := g.cdb.Quick().SearchTextOnly(ctx, ftsQuery(question), len(g.model.Metrics))
	if err != nil {
		return nil, err
	}
	lex := map[string]float64{}
	for _, h := range hits {
		name := h.Metadata["name"]
		if name == "" {
			name = h.ID
		}
		lex[name] = h.Score
	}

	// Dense signal: cosine of the question against each metric embedding.
	dense := map[string]float64{}
	if g.emb != nil && len(g.mvecs) > 0 {
		qv, err := embedUnit(ctx, g.emb, question)
		if err != nil {
			return nil, fmt.Errorf("embed question: %w", err)
		}
		for name, mv := range g.mvecs {
			dense[name] = dot(qv, mv)
		}
	}

	// Fuse: min-max normalize each signal, then weighted sum. Dense leads when
	// available (semantics > keywords); BM25-only when there's no embedder.
	lexN, denseN := minmax(lex), minmax(dense)
	memLex := g.memLexical(question) // CJK/ASCII-aware, in-memory; independent of the FTS tokenizer
	const wLex, wDense = 0.4, 0.6
	combined := map[string]float64{}
	for i := range g.model.Metrics {
		name := g.model.Metrics[i].Name
		if g.emb != nil {
			s := wLex*lexN[name] + wDense*denseN[name]
			if memLex[name] > s { // an exact lexical (incl. CJK) match floors the score
				s = memLex[name]
			}
			combined[name] = s
		} else {
			// BM25-only (offline): fuse cortexdb lexical with the in-memory CJK-aware
			// signal, so Chinese questions retrieve even when the FTS index drops CJK.
			s := lexN[name]
			if memLex[name] > s {
				s = memLex[name]
			}
			if s > 0 {
				combined[name] = s
			}
		}
	}

	out := make([]ScoredMetric, 0, len(combined))
	for name, score := range combined {
		out = append(out, ScoredMetric{Name: name, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > g.topK {
		out = out[:g.topK]
	}
	// Cross-encoder rerank: re-score the top candidates jointly with the
	// question (name/synonym coverage + description overlap) so an exact match
	// outranks a merely topically-near neighbor — the precision@1 stage.
	if g.reranker != nil {
		out = g.reranker.Rerank(question, g.model, out)
	}
	return out, nil
}

// Ground retrieves relevant metrics, then emits a semantic query (or a Clarify).
func (g *Grounder) Ground(ctx context.Context, question string) (semantic.Query, []ScoredMetric, *Clarify, error) {
	return g.GroundWithFeedback(ctx, question, "")
}

// GroundWithFeedback is Ground with a critic's revise feedback folded into the
// LLM prompt (the plan-query-critique loop feeds the critic's
// reason forward so the next attempt fixes the named problem). Empty feedback
// behaves exactly like Ground.
func (g *Grounder) GroundWithFeedback(ctx context.Context, question, feedback string) (semantic.Query, []ScoredMetric, *Clarify, error) {
	retrieved, err := g.Retrieve(ctx, question)
	if err != nil {
		return semantic.Query{}, nil, nil, err
	}
	if len(retrieved) == 0 {
		return semantic.Query{}, retrieved, &Clarify{Question: "No matching metric. Try naming the measure.", Candidates: g.model.MetricNames()}, nil
	}
	if g.llm == nil {
		q, err := g.keyword(question, retrieved)
		return q, retrieved, nil, err
	}
	q, cl, err := g.llmGround(ctx, question, retrieved, feedback)
	return q, retrieved, cl, err
}

// --- LLM grounding over the retrieved (pruned) metric set ---

func (g *Grounder) llmGround(ctx context.Context, question string, retrieved []ScoredMetric, feedback string) (semantic.Query, *Clarify, error) {
	var b strings.Builder
	b.WriteString(`Translate the question into a semantic query over the FIXED catalog below.
Return STRICTLY JSON: {"metrics":["..."],"group_by":["..."],"where":[{"dimension":"","op":"=","values":["..."]}],"time_grain":"","clarify":"","candidates":["..."]}
Use ONLY names from the catalog. To restrict to specific dimension values, add a "where" entry (op one of =,!=,>,>=,<,<=,in).
If the question is ambiguous between metrics, leave "metrics" empty and set "clarify" (a short question) + "candidates".
Never invent names; never write SQL.

METRICS (retrieved as most relevant):
`)
	for _, r := range retrieved {
		m := g.model.Metric(r.Name)
		if m == nil {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s", m.Name, m.Description)
		if len(m.Synonyms) > 0 {
			fmt.Fprintf(&b, " (synonyms: %s)", strings.Join(m.Synonyms, ", "))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nDIMENSIONS:\n")
	for i := range g.model.Dimensions {
		d := &g.model.Dimensions[i]
		fmt.Fprintf(&b, "- %s (%s)\n", d.Name, d.Type)
	}

	// Few-shot: inject the most similar (question → query) exemplars so the model
	// generalizes from patterns. Diversity beats volume — the bank dedups by shape.
	if exs := g.bank.Retrieve(ctx, question, 3); len(exs) > 0 {
		b.WriteString("\nEXAMPLES (question → JSON):\n")
		for _, ex := range exs {
			q := ex.Query()
			js, _ := json.Marshal(map[string]any{"metrics": q.Metrics, "group_by": q.GroupBy, "time_grain": q.TimeGrain})
			fmt.Fprintf(&b, "Q: %s\n%s\n", ex.Question, js)
		}
	}

	if feedback != "" {
		fmt.Fprintf(&b, "\nThe PREVIOUS attempt was rejected by the critic. Fix exactly this and try again:\n%s\n", feedback)
	}

	fmt.Fprintf(&b, "\nQUESTION: %s\nJSON:", question)
	return g.askForQuery(ctx, b.String(), retrieved)
}

// askForQuery sends a prompt that must return the {metrics,group_by,time_grain,
// clarify,candidates} JSON and parses it into a Query or a Clarify.
func (g *Grounder) askForQuery(ctx context.Context, body string, retrieved []ScoredMetric) (semantic.Query, *Clarify, error) {
	raw, err := g.llm.Ask(ctx, body)
	if err != nil {
		return semantic.Query{}, nil, err
	}
	js, err := extractJSON(raw)
	if err != nil {
		return semantic.Query{}, nil, fmt.Errorf("grounder returned non-JSON: %w", err)
	}
	var out struct {
		Metrics    []string `json:"metrics"`
		GroupBy    []string `json:"group_by"`
		TimeGrain  string   `json:"time_grain"`
		Clarify    string   `json:"clarify"`
		Candidates []string `json:"candidates"`
		Where      []struct {
			Dimension string `json:"dimension"`
			Op        string `json:"op"`
			Values    []any  `json:"values"`
		} `json:"where"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return semantic.Query{}, nil, fmt.Errorf("parse semantic query: %w", err)
	}
	if out.Clarify != "" || len(out.Metrics) == 0 {
		c := &Clarify{Question: out.Clarify, Candidates: out.Candidates}
		if c.Question == "" {
			c.Question = "Which metric did you mean?"
		}
		if len(c.Candidates) == 0 {
			for _, r := range retrieved {
				c.Candidates = append(c.Candidates, r.Name)
			}
		}
		return semantic.Query{}, c, nil
	}
	q := semantic.Query{Metrics: out.Metrics, GroupBy: out.GroupBy, TimeGrain: out.TimeGrain}
	for _, f := range out.Where {
		if f.Dimension == "" || len(f.Values) == 0 {
			continue
		}
		op := f.Op
		if op == "" {
			op = "="
		}
		q.Where = append(q.Where, semantic.Filter{Dimension: f.Dimension, Op: op, Values: f.Values})
	}
	return q, nil, nil
}

// GroundFollowup resolves a message in the context of a prior query. The LLM is
// told the previous query and decides whether
// this is a refinement (merge: keep prior fields the message doesn't change) or a
// new topic (replace). Without an LLM it falls back to a field-level merge: a
// fragment that only names dimensions/grain inherits the prior metrics.
func (g *Grounder) GroundFollowup(ctx context.Context, question string, prior semantic.Query) (semantic.Query, *Clarify, error) {
	retrieved, err := g.Retrieve(ctx, question)
	if err != nil {
		return semantic.Query{}, nil, err
	}
	if g.llm == nil {
		return g.keywordMerge(question, prior, retrieved)
	}

	var b strings.Builder
	b.WriteString(`You maintain a conversational analytics query. Given the PREVIOUS query and a new user message,
output the NEW query as STRICT JSON {"metrics":[],"group_by":[],"where":[{"dimension":"","op":"=","values":["..."]}],"time_grain":"","clarify":"","candidates":[]}.
Rules: if the message REFINES the prior query (adds a slice, a filter like "just the South", a time grain, swaps one
field), MERGE — keep prior fields the message doesn't change and carry prior "where" unless the message changes it.
If it's a NEW topic, REPLACE. Use ONLY catalog names; never invent.

`)
	priorWhere := make([]map[string]any, 0, len(prior.Where))
	for _, f := range prior.Where {
		priorWhere = append(priorWhere, map[string]any{"dimension": f.Dimension, "op": f.Op, "values": f.Values})
	}
	pj, _ := json.Marshal(map[string]any{"metrics": prior.Metrics, "group_by": prior.GroupBy, "where": priorWhere, "time_grain": prior.TimeGrain})
	fmt.Fprintf(&b, "PREVIOUS QUERY: %s\n\nMETRICS:\n", pj)
	for _, r := range retrieved {
		if m := g.model.Metric(r.Name); m != nil {
			fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
		}
	}
	b.WriteString("DIMENSIONS:\n")
	for i := range g.model.Dimensions {
		d := &g.model.Dimensions[i]
		fmt.Fprintf(&b, "- %s (%s)\n", d.Name, d.Type)
	}
	fmt.Fprintf(&b, "\nNEW MESSAGE: %s\nJSON:", question)
	return g.askForQuery(ctx, b.String(), retrieved)
}

// keywordMerge is the offline follow-up: ground the message standalone, then if
// it named no metric (a pure refinement) inherit the prior metrics and merge the
// new dimensions; otherwise treat it as a new topic.
func (g *Grounder) keywordMerge(question string, prior semantic.Query, retrieved []ScoredMetric) (semantic.Query, *Clarify, error) {
	q, err := g.keyword(question, retrieved)
	if err != nil || len(prior.Metrics) == 0 {
		return q, nil, err
	}
	named := false
	ql := strings.ToLower(question)
	for _, m := range g.model.Metrics {
		if hasPhrase(ql, normalize(m.Name)) {
			named = true
			break
		}
	}
	if named { // new topic
		return q, nil, nil
	}
	// Refinement: keep prior metrics, merge new group_by.
	merged := semantic.Query{Metrics: prior.Metrics, GroupBy: dedup(append(append([]string{}, prior.GroupBy...), q.GroupBy...)), TimeGrain: prior.TimeGrain}
	return merged, nil, nil
}

// AskLLM exposes the grounder's LLM for higher-level planners (e.g. multi-metric
// chaining). ok is false when no LLM is configured.
func (g *Grounder) AskLLM(ctx context.Context, prompt string) (out string, ok bool, err error) {
	if g.llm == nil {
		return "", false, nil
	}
	s, err := g.llm.Ask(ctx, prompt)
	return s, true, err
}

// Model returns the semantic model the grounder is bound to.
func (g *Grounder) Model() *semantic.Model { return g.model }

// --- offline keyword fallback over the retrieved set ---

func (g *Grounder) keyword(question string, retrieved []ScoredMetric) (semantic.Query, error) {
	q := strings.ToLower(question)

	// Longest-phrase-first with span consumption: a specific phrase ("net
	// revenue") claims its span so a generic synonym ("revenue") that overlaps
	// it can't also fire. This is the offline analogue of disambiguation — it
	// stops one question from resolving to two competing metrics.
	type cand struct{ metric, phrase string }
	var cands []cand
	for _, r := range retrieved {
		m := g.model.Metric(r.Name)
		if m == nil {
			continue
		}
		cands = append(cands, cand{m.Name, normalize(m.Name)})
		for _, syn := range m.Synonyms {
			cands = append(cands, cand{m.Name, normalize(syn)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return len(cands[i].phrase) > len(cands[j].phrase) })

	consumed := make([]bool, len(q))
	seen := map[string]bool{}
	var metrics []string
	for _, c := range cands {
		if seen[c.metric] || c.phrase == "" {
			continue
		}
		if at := findSpan(q, c.phrase, consumed); at >= 0 {
			for i := at; i < at+len(c.phrase); i++ {
				consumed[i] = true
			}
			metrics = append(metrics, c.metric)
			seen[c.metric] = true
		}
	}
	if len(metrics) == 0 && len(retrieved) > 0 {
		metrics = []string{retrieved[0].Name} // best retrieval hit
	}

	var groupBy []string
	for i := range g.model.Dimensions {
		d := &g.model.Dimensions[i]
		labels := append([]string{d.Name, d.Column}, d.Synonyms...)
		for _, lab := range labels {
			if phraseHit(q, normalize(lab)) {
				groupBy = append(groupBy, d.Name)
				break
			}
		}
	}
	if len(metrics) == 0 {
		return semantic.Query{}, errors.New("no metric matched")
	}
	return semantic.Query{Metrics: dedup(metrics), GroupBy: dedup(groupBy)}, nil
}

func normalize(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", " ")) }

// findSpan returns the start index of phrase in q at word boundaries and not
// overlapping an already-consumed span, or -1.
func findSpan(q, phrase string, consumed []bool) int {
	for from := 0; from < len(q); {
		i := strings.Index(q[from:], phrase)
		if i < 0 {
			return -1
		}
		at := from + i
		end := at + len(phrase)
		if wordBoundary(q, at, end) && !overlaps(consumed, at, end) {
			return at
		}
		from = at + 1
	}
	return -1
}

func hasPhrase(q, phrase string) bool {
	return phrase != "" && findSpan(q, phrase, make([]bool, len(q))) >= 0
}

func overlaps(consumed []bool, at, end int) bool {
	for i := at; i < end && i < len(consumed); i++ {
		if consumed[i] {
			return true
		}
	}
	return false
}

func wordBoundary(q string, at, end int) bool {
	if at > 0 && isWordByte(q[at-1]) {
		return false
	}
	if end < len(q) && isWordByte(q[end]) {
		return false
	}
	return true
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// --- helpers ---

var wordRe = regexp.MustCompile(`[A-Za-z0-9]+`)

var stop = map[string]bool{"the": true, "a": true, "an": true, "of": true, "for": true,
	"by": true, "in": true, "to": true, "and": true, "what": true, "is": true, "are": true,
	"how": true, "many": true, "show": true, "me": true, "per": true, "each": true}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

func hasCJKText(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}

// phraseHit reports whether phrase occurs in q. ASCII phrases use word-boundary
// matching (so "region" doesn't hit "regional"); CJK phrases, which have no word
// boundaries, use a plain substring test.
func phraseHit(q, phrase string) bool {
	if phrase == "" {
		return false
	}
	if hasCJKText(phrase) {
		return strings.Contains(q, phrase)
	}
	return hasPhrase(q, phrase)
}

// tokensOf lowercases s and returns a token set: ASCII [a-z0-9]+ runs (len>1,
// non-stop) plus overlapping CJK character bigrams (a lone CJK char becomes a
// unigram). This gives BM25/lexical matching for Chinese/Japanese/Korean without
// a word segmenter — "各门店大区的营收" → 各门,门店,店大,大区,区的,的营,营收.
func tokensOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if !stop[w] && len(w) > 1 {
			out[w] = true
		}
	}
	var run []rune
	flush := func() {
		if len(run) == 1 {
			out[string(run)] = true
		}
		for i := 0; i+1 < len(run); i++ {
			out[string(run[i:i+2])] = true
		}
		run = run[:0]
	}
	for _, r := range s {
		if isCJK(r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// memLexical scores each metric by CJK/ASCII token overlap between the question
// and the metric's name + synonyms + description. Pure in-memory over the (small)
// model, so lexical retrieval works even with no embedder and regardless of the
// FTS engine's own tokenizer.
func (g *Grounder) memLexical(question string) map[string]float64 {
	q := tokensOf(question)
	out := map[string]float64{}
	if len(q) == 0 {
		return out
	}
	for i := range g.model.Metrics {
		m := &g.model.Metrics[i]
		d := tokensOf(m.Name + " " + strings.Join(m.Synonyms, " ") + " " + m.Description)
		hit := 0
		for t := range q {
			if d[t] {
				hit++
			}
		}
		if hit > 0 {
			out[m.Name] = float64(hit) / float64(len(q))
		}
	}
	return out
}

func ftsQuery(q string) string {
	toks := tokensOf(q)
	if len(toks) == 0 {
		return strings.TrimSpace(q)
	}
	words := make([]string, 0, len(toks))
	for t := range toks {
		words = append(words, t)
	}
	return strings.Join(words, " OR ")
}

func contains(hay, term string) bool {
	term = strings.ToLower(strings.ReplaceAll(term, "_", " "))
	return term != "" && strings.Contains(hay, term)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func extractJSON(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", errors.New("no JSON object")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("unbalanced JSON")
}

var _ = os.Getenv
