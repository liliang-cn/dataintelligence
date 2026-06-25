package nleval

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
)

// Grader runs each question through the real grounding → governance → engine
// path and scores the answer on the three axes.
type Grader struct {
	Eng *engine.Engine
	Gr  *grounding.Grounder
	Pol governance.Policy
	Tol float64 // relative tolerance for result match (default 1e-6)
}

// Check is one graded axis.
type Check struct {
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// CaseResult is the full grade for one case.
type CaseResult struct {
	Case      string   `json:"case"`
	Category  string   `json:"category"`
	Question  string   `json:"question"`
	Expected  []string `json:"expected"`
	Predicted []string `json:"predicted"`
	PredDims  []string `json:"pred_dims"`

	Semantic  *Check `json:"semantic,omitempty"`  // right metric + dims (diagnosis)
	Execution *Check `json:"execution,omitempty"` // governed query ran
	Result    *Check `json:"result,omitempty"`    // numbers equal the control
	Clarify   *Check `json:"clarify,omitempty"`   // ambiguity asked back
	Refusal   *Check `json:"refusal,omitempty"`   // governance refused

	Answer  string   `json:"-"` // rendered answer (for the eval-go judge layer)
	Context []string `json:"-"` // metric descriptions the answer is grounded on
	Skipped bool     `json:"skipped"`
	Passed  bool     `json:"passed"`
}

// Run grades every case. llmWired controls whether needs_llm cases are included.
func (g *Grader) Run(ctx context.Context, ds *Dataset, llmWired bool) *Report {
	if g.Tol <= 0 {
		g.Tol = 1e-6
	}
	rep := &Report{Tol: g.Tol}
	for _, c := range ds.Cases {
		rep.Cases = append(rep.Cases, g.grade(ctx, c, llmWired))
	}
	rep.finalize()
	return rep
}

func (g *Grader) grade(ctx context.Context, c Case, llmWired bool) CaseResult {
	cr := CaseResult{Case: c.Name, Category: c.Category, Question: c.Question, Expected: c.ExpectMetrics}
	if c.NeedsLLM && !llmWired {
		cr.Skipped = true
		return cr
	}

	q, _, clar, err := g.Gr.Ground(ctx, c.Question)
	if clar != nil {
		cr.Predicted = clar.Candidates
	} else {
		cr.Predicted = q.Metrics
		cr.PredDims = q.GroupBy
	}

	// Ambiguity probe: the grounder must ask back instead of guessing.
	if c.ExpectClarify {
		cr.Clarify = &Check{Pass: clar != nil}
		if clar != nil {
			cr.Clarify.Detail = "asked: " + clar.Question
		} else {
			cr.Clarify.Detail = "guessed instead of clarifying"
		}
		cr.Passed = cr.Clarify.Pass
		return cr
	}
	if err != nil && clar == nil {
		cr.Execution = &Check{Pass: false, Detail: "ground: " + err.Error()}
		return cr
	}
	if clar != nil {
		cr.Semantic = &Check{Pass: false, Detail: "unexpected clarify: " + clar.Question}
		return cr
	}

	// Semantic match (diagnosis — reported even when result match is the gate).
	semOK := setEq(q.Metrics, c.ExpectMetrics) && setEq(q.GroupBy, c.ExpectDims)
	cr.Semantic = &Check{Pass: semOK, Detail: fmt.Sprintf("metrics %v vs %v · dims %v vs %v",
		q.Metrics, c.ExpectMetrics, orEmpty(q.GroupBy), orEmpty(c.ExpectDims))}
	cr.Context = g.contextFor(q.Metrics)

	// Execute through the governance boundary.
	role := c.Role
	if role == "" {
		role = "admin"
	}
	p := governance.Principal{User: "eval", Role: role, Attrs: map[string]string{"region": "South"}}
	ans, qerr := governance.Query(ctx, g.Eng, q, p, g.Pol)

	// Governance probe: an unauthorized metric must be refused.
	if c.ExpectRefused {
		cr.Refusal = &Check{Pass: qerr != nil}
		if qerr != nil {
			cr.Refusal.Detail = "refused: " + qerr.Error()
		} else {
			cr.Refusal.Detail = "LEAK: answered an unauthorized metric"
		}
		cr.Passed = cr.Refusal.Pass
		return cr
	}
	if qerr != nil {
		cr.Execution = &Check{Pass: false, Detail: qerr.Error()}
		return cr
	}
	cr.Execution = &Check{Pass: true, Detail: fmt.Sprintf("%d rows in %dms", len(ans.Rows), ans.ExecMs)}
	cr.Answer = renderAnswer(ans)

	// Result match vs the control query (the headline grade).
	if c.Control != "" {
		ok, detail := g.resultMatch(ctx, c, ans)
		cr.Result = &Check{Pass: ok, Detail: detail}
	}

	// Gate: result match when there's a control, else fall back to semantic.
	if cr.Result != nil {
		cr.Passed = cr.Execution.Pass && cr.Result.Pass
	} else {
		cr.Passed = cr.Execution.Pass && cr.Semantic.Pass
	}
	return cr
}

func (g *Grader) contextFor(metrics []string) []string {
	var out []string
	for _, name := range metrics {
		if m := g.Eng.Model.Metric(name); m != nil {
			out = append(out, m.Name+": "+m.Description)
		}
	}
	return out
}

// resultMatch compares the governed answer to the control query order-insensitively:
// numeric cells (sums, counts) must match within tolerance; label cells (regions,
// dates) must match as sets.
func (g *Grader) resultMatch(ctx context.Context, c Case, ans *engine.Answer) (bool, string) {
	ctrl, err := g.Eng.WH.Query(ctx, c.Control)
	if err != nil {
		return false, "control error: " + err.Error()
	}
	gotNums, gotLabels := split(ans.Rows)
	wantNums, wantLabels := split(ctrl.Rows)
	if !floatsEq(gotNums, wantNums, g.Tol) {
		return false, fmt.Sprintf("numbers got=%s want=%s", fmtNums(gotNums), fmtNums(wantNums))
	}
	if !stringsEq(gotLabels, wantLabels) {
		return false, fmt.Sprintf("labels got=%v want=%v", gotLabels, wantLabels)
	}
	return true, fmt.Sprintf("%d numeric cell(s) match (±%.0e)", len(gotNums), g.Tol)
}

// --- value comparison ---

// split classifies every cell in a result into numeric vs label buckets.
func split(rows [][]any) (nums []float64, labels []string) {
	for _, row := range rows {
		for _, cell := range row {
			if cell == nil {
				continue
			}
			s := strings.TrimSpace(fmt.Sprintf("%v", cell))
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				nums = append(nums, f)
			} else {
				labels = append(labels, s)
			}
		}
	}
	return nums, labels
}

func floatsEq(a, b []float64, tol float64) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Float64s(a)
	sort.Float64s(b)
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		scale := absf(a[i])
		if absf(b[i]) > scale {
			scale = absf(b[i])
		}
		if scale < 1 {
			scale = 1
		}
		if d/scale > tol {
			return false
		}
	}
	return true
}

func stringsEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func setEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func orEmpty(s []string) any {
	if len(s) == 0 {
		return "[]"
	}
	return s
}

func fmtNums(ns []float64) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.FormatFloat(n, 'g', 8, 64)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// renderAnswer turns a result into a one-line natural answer for the judge layer.
func renderAnswer(ans *engine.Answer) string {
	if len(ans.Rows) == 0 {
		return "(no rows)"
	}
	if len(ans.Rows) == 1 && len(ans.Columns) == 1 {
		return fmt.Sprintf("%s = %v", ans.Columns[0], ans.Rows[0][0])
	}
	var b strings.Builder
	b.WriteString(strings.Join(ans.Columns, ", "))
	b.WriteString(": ")
	limit := len(ans.Rows)
	if limit > 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteString("; ")
		}
		cells := make([]string, len(ans.Rows[i]))
		for j, c := range ans.Rows[i] {
			cells[j] = fmt.Sprintf("%v", c)
		}
		b.WriteString(strings.Join(cells, "="))
	}
	if len(ans.Rows) > limit {
		fmt.Fprintf(&b, "; …(%d more)", len(ans.Rows)-limit)
	}
	return b.String()
}
