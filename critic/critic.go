// Package critic is the formal verification step of the agentic loop: after a
// question is grounded and executed, a critic judges the result along four fixed
// dimensions — grain, coverage, sanity, and metric-identity — and returns a
// verdict: pass | revise | ask_user. The loop driver (loop.go) feeds a revise
// reason forward and retries with a bound, so a plausible-but-wrong answer is
// caught in the layer instead of shipped silently.
package critic

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
)

// Decision is the critic's verdict.
type Decision string

const (
	Pass    Decision = "pass"
	Revise  Decision = "revise"
	AskUser Decision = "ask_user"
)

// Verdict is one critique outcome with the dimension that triggered it and a
// feedback line the grounder can act on.
type Verdict struct {
	Decision  Decision `json:"decision"`
	Dimension string   `json:"dimension,omitempty"` // grain | coverage | sanity | metric-identity
	Reasons   []string `json:"reasons,omitempty"`
	Feedback  string   `json:"feedback,omitempty"`
	By        string   `json:"by"` // which critic produced it (rule | llm)
}

// Input is everything the critic sees about one attempt.
type Input struct {
	Question string
	Query    semantic.Query
	Model    *semantic.Model
	Answer   *engine.Answer
}

// Critic judges one attempt.
type Critic interface {
	Critique(ctx context.Context, in Input) Verdict
}

// RuleCritic is a deterministic, zero-cost critic. It catches the failure modes
// that don't need a model: missing breakdown/time slots (coverage), impossible
// values (sanity), and a question that names a metric the query didn't use
// (metric-identity).
type RuleCritic struct{}

func (RuleCritic) Critique(_ context.Context, in Input) Verdict {
	q := strings.ToLower(in.Question)
	var reasons []string
	dim := ""
	add := func(d, r string) {
		if dim == "" {
			dim = d
		}
		reasons = append(reasons, d+": "+r)
	}

	// --- coverage: did the query answer every clause of the question? ---
	if dim := mentionsDimBreakdown(in.Model, q); dim != "" && len(in.Query.GroupBy) == 0 {
		add("coverage", fmt.Sprintf("the question asks to break down by %q but the query has no group_by", dim))
	}
	if mentionsTime(q) && in.Query.TimeGrain == "" && !hasTimeDim(in.Model, in.Query.GroupBy) && !hasTimeFilter(in.Model, in.Query.Where) {
		add("coverage", "the question mentions a time period but the query has no time grain, time group_by, or time filter")
	}

	// --- metric-identity: did we use the metric the question names? ---
	if want, got, ok := identityMismatch(in.Model, in.Question, in.Query.Metrics); ok {
		add("metric-identity", fmt.Sprintf("the question points at %q but the query uses %v", want, got))
	}

	// --- sanity: are the numbers possible? ---
	if in.Answer != nil {
		if len(in.Answer.Rows) == 0 {
			// Empty isn't always wrong (RLS, real gaps) — ask the user rather than loop.
			return Verdict{Decision: AskUser, Dimension: "sanity", By: "rule",
				Reasons:  []string{"sanity: the query returned 0 rows"},
				Feedback: "No rows came back — confirm the filters/time range or whether data exists."}
		}
		for _, r := range sanityNegatives(in.Model, in.Query.Metrics, in.Answer) {
			add("sanity", r)
		}
	}

	if len(reasons) == 0 {
		return Verdict{Decision: Pass, By: "rule"}
	}
	return Verdict{Decision: Revise, Dimension: dim, Reasons: reasons, By: "rule",
		Feedback: strings.Join(reasons, "; ")}
}

// --- heuristics ---

var (
	breakdownRe = regexp.MustCompile(`\b(by|per|each|breakdown|split|across|grouped)\b`)
	timeRe      = regexp.MustCompile(`\b(last|this|previous|prior|quarter|month|monthly|year|yearly|annual|week|weekly|daily|day|ytd|mtd|qtd|yoy|mom|trend|over time|since|between|20\d\d)\b`)
)

func mentionsTime(q string) bool { return timeRe.MatchString(q) }

// mentionsDimBreakdown returns the dimension a question asks to break down by —
// only when a breakdown keyword AND an actual dimension token are present. This
// keeps "revenue per order" (a ratio, "order" is no dimension) from being read
// as a missing group_by, while "revenue by region" still is.
func mentionsDimBreakdown(m *semantic.Model, q string) string {
	if !breakdownRe.MatchString(q) {
		return ""
	}
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		if wordIn(q, norm(d.Name)) || wordIn(q, norm(d.Column)) {
			return d.Name
		}
	}
	return ""
}

func hasTimeDim(m *semantic.Model, groupBy []string) bool {
	for _, name := range groupBy {
		if d := m.Dimension(name); d != nil && d.Type == "time" {
			return true
		}
	}
	return false
}

func hasTimeFilter(m *semantic.Model, where []semantic.Filter) bool {
	for _, f := range where {
		if d := m.Dimension(f.Dimension); d != nil && d.Type == "time" {
			return true
		}
	}
	return false
}

// identityMismatch reports when the question contains a strong metric phrase
// (a metric name or synonym) whose metric is NOT among the query's metrics.
func identityMismatch(m *semantic.Model, question string, used []string) (want string, got []string, ok bool) {
	q := strings.ToLower(question)
	inUse := map[string]bool{}
	for _, u := range used {
		inUse[u] = true
	}
	// Longest phrase first so "net revenue" wins over "revenue".
	type cand struct{ metric, phrase string }
	var cands []cand
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		cands = append(cands, cand{mt.Name, norm(mt.Name)})
		for _, s := range mt.Synonyms {
			cands = append(cands, cand{mt.Name, norm(s)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return len(cands[i].phrase) > len(cands[j].phrase) })
	for _, c := range cands {
		if c.phrase == "" || !wordIn(q, c.phrase) {
			continue
		}
		if !inUse[c.metric] {
			return c.metric, used, true
		}
		return "", nil, false // the strongest phrase IS in use → identity ok
	}
	return "", nil, false
}

// sanityNegatives flags negative values for additive (sum/count) base metrics —
// window deltas and derived metrics may legitimately go negative, so skip them.
func sanityNegatives(m *semantic.Model, metrics []string, ans *engine.Answer) []string {
	var out []string
	col := map[string]int{}
	for i, c := range ans.Columns {
		col[c] = i
	}
	for _, name := range metrics {
		mt := m.Metric(name)
		if mt == nil || mt.IsWindow() || mt.IsDerived() {
			continue
		}
		if mt.Agg != "sum" && mt.Agg != "count" && mt.Agg != "count_distinct" {
			continue
		}
		ci, ok := col[name]
		if !ok {
			continue
		}
		for _, row := range ans.Rows {
			if ci < len(row) && toFloat(row[ci]) < 0 {
				out = append(out, fmt.Sprintf("%s is negative (%v) but it is an additive measure", name, row[ci]))
				break
			}
		}
	}
	return out
}

func norm(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", " ")) }

// wordIn reports whether phrase appears in q at word boundaries.
func wordIn(q, phrase string) bool {
	for from := 0; from < len(q); {
		i := strings.Index(q[from:], phrase)
		if i < 0 {
			return false
		}
		at := from + i
		end := at + len(phrase)
		lb := at == 0 || !isWord(q[at-1])
		rb := end >= len(q) || !isWord(q[end])
		if lb && rb {
			return true
		}
		from = at + 1
	}
	return false
}

func isWord(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	default:
		var f float64
		_, _ = fmt.Sscanf(fmt.Sprintf("%v", t), "%g", &f)
		return f
	}
}
