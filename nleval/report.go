package nleval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Report is the full outcome of an eval run.
type Report struct {
	Cases      []CaseResult     `json:"cases"`
	Categories []CategoryStat   `json:"categories"`
	Confusion  []ConfusionEntry `json:"confusion"`
	Tol        float64          `json:"tol"`

	Total   int     `json:"total"`   // graded (excludes skipped)
	Passed  int     `json:"passed"`  //
	Skipped int     `json:"skipped"` //
	Acc     float64 `json:"accuracy"`

	// Cost benchmark: end-to-end latency across graded cases.
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`
	MaxMs int64 `json:"max_ms"`
}

// CategoryStat is per-category accuracy (the per-category floor is checked here).
type CategoryStat struct {
	Category string  `json:"category"`
	Passed   int     `json:"passed"`
	Total    int     `json:"total"`
	Acc      float64 `json:"accuracy"`
}

// ConfusionEntry records expected→predicted metric pairs (diagnosis of which
// metrics the grounder mixes up).
type ConfusionEntry struct {
	Expected  string `json:"expected"`
	Predicted string `json:"predicted"`
	Count     int    `json:"count"`
	Correct   bool   `json:"correct"`
}

func (r *Report) finalize() {
	catAcc := map[string]*CategoryStat{}
	var catOrder []string
	conf := map[string]int{}
	confCorrect := map[string]bool{}

	for _, c := range r.Cases {
		if c.Skipped {
			r.Skipped++
			continue
		}
		r.Total++
		if c.Passed {
			r.Passed++
		}
		cs := catAcc[c.Category]
		if cs == nil {
			cs = &CategoryStat{Category: c.Category}
			catAcc[c.Category] = cs
			catOrder = append(catOrder, c.Category)
		}
		cs.Total++
		if c.Passed {
			cs.Passed++
		}
		// Confusion: expected[0] → predicted[0].
		exp, pred := first(c.Expected), first(c.Predicted)
		if exp != "" {
			key := exp + "→" + pred
			conf[key]++
			confCorrect[key] = exp == pred
		}
	}
	if r.Total > 0 {
		r.Acc = float64(r.Passed) / float64(r.Total)
	}

	// Latency percentiles over graded cases (the cost benchmark).
	var lat []int64
	for _, c := range r.Cases {
		if !c.Skipped {
			lat = append(lat, c.LatencyMs)
		}
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		pct := func(p float64) int64 {
			idx := int(p * float64(len(lat)-1))
			return lat[idx]
		}
		r.P50Ms, r.P95Ms, r.MaxMs = pct(0.50), pct(0.95), lat[len(lat)-1]
	}

	sort.Strings(catOrder)
	for _, name := range catOrder {
		cs := catAcc[name]
		if cs.Total > 0 {
			cs.Acc = float64(cs.Passed) / float64(cs.Total)
		}
		r.Categories = append(r.Categories, *cs)
	}

	var keys []string
	for k := range conf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.SplitN(k, "→", 2)
		r.Confusion = append(r.Confusion, ConfusionEntry{
			Expected: parts[0], Predicted: parts[1], Count: conf[k], Correct: confCorrect[k],
		})
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// Gate reports whether the run clears the overall floor and every per-category
// floor; the failing reasons are returned for the console.
func (r *Report) Gate(minAcc float64, floors map[string]float64) (bool, []string) {
	var fails []string
	if r.Acc < minAcc {
		fails = append(fails, fmt.Sprintf("overall accuracy %.0f%% < min %.0f%%", r.Acc*100, minAcc*100))
	}
	for _, cs := range r.Categories {
		floor, ok := floors[cs.Category]
		if !ok {
			floor = minAcc
		}
		if cs.Acc < floor {
			fails = append(fails, fmt.Sprintf("category %q %.0f%% < floor %.0f%%", cs.Category, cs.Acc*100, floor*100))
		}
	}
	return len(fails) == 0, fails
}

// WriteConsole prints the per-case grid, per-category accuracy, and confusion.
func (r *Report) WriteConsole(w io.Writer) {
	fmt.Fprintln(w, "=== NL eval report ===")
	for _, c := range r.Cases {
		status := "PASS"
		switch {
		case c.Skipped:
			status = "SKIP"
		case !c.Passed:
			status = "FAIL"
		}
		fmt.Fprintf(w, "\n[%s] %-32s (%s)\n        Q: %s\n", status, c.Case, c.Category, c.Question)
		for _, x := range []struct {
			name string
			chk  *Check
		}{
			{"semantic", c.Semantic}, {"execution", c.Execution}, {"result", c.Result},
			{"clarify", c.Clarify}, {"refusal", c.Refusal},
		} {
			if x.chk == nil {
				continue
			}
			mark := "✓"
			if !x.chk.Pass {
				mark = "✗"
			}
			fmt.Fprintf(w, "        %s %-10s %s\n", mark, x.name, x.chk.Detail)
		}
	}

	fmt.Fprintln(w, "\n--- per-category accuracy ---")
	for _, cs := range r.Categories {
		fmt.Fprintf(w, "  %-12s %d/%d (%.0f%%)\n", cs.Category, cs.Passed, cs.Total, cs.Acc*100)
	}

	fmt.Fprintln(w, "\n--- metric confusion (expected → predicted) ---")
	for _, e := range r.Confusion {
		mark := " "
		if !e.Correct {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %-22s → %-22s ×%d\n", mark, e.Expected, e.Predicted, e.Count)
	}

	fmt.Fprintln(w, "\n--- cost benchmark (end-to-end latency) ---")
	fmt.Fprintf(w, "  p50 %dms · p95 %dms · max %dms\n", r.P50Ms, r.P95Ms, r.MaxMs)

	fmt.Fprintf(w, "\nOverall: %d/%d (%.0f%%) graded · %d skipped\n", r.Passed, r.Total, r.Acc*100, r.Skipped)
}

// WriteJSON emits the machine-readable report (CI artifact / dashboard feed).
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Save persists per-case rows + a run summary to the accuracy dashboard tables.
// runNs is a caller-provided nanosecond clock so the package stays test-friendly.
func (r *Report) Save(ctx context.Context, wh *warehouse.Warehouse, runNs int64) (string, error) {
	runID := fmt.Sprintf("nleval-%d", runNs)
	if _, err := wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _nl_eval_runs (
		run_id text PRIMARY KEY, total int, passed int, accuracy double precision,
		doc jsonb, at timestamptz DEFAULT now())`); err != nil {
		return "", err
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _nl_eval_cases (
		run_id text, "case" text, category text, passed bool, semantic bool, execution bool,
		result bool, detail text, at timestamptz DEFAULT now())`); err != nil {
		return "", err
	}
	doc, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if _, err := wh.Exec(ctx, `INSERT INTO _nl_eval_runs (run_id,total,passed,accuracy,doc) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (run_id) DO UPDATE SET total=$2,passed=$3,accuracy=$4,doc=$5`,
		runID, r.Total, r.Passed, r.Acc, string(doc)); err != nil {
		return "", err
	}
	for _, c := range r.Cases {
		if c.Skipped {
			continue
		}
		if _, err := wh.Exec(ctx,
			`INSERT INTO _nl_eval_cases (run_id,"case",category,passed,semantic,execution,result,detail)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			runID, c.Case, c.Category, c.Passed, chkPass(c.Semantic), chkPass(c.Execution), chkPass(c.Result),
			c.caseDetail()); err != nil {
			return "", err
		}
	}
	return runID, nil
}

func chkPass(c *Check) bool { return c != nil && c.Pass }

func (c CaseResult) caseDetail() string {
	for _, chk := range []*Check{c.Result, c.Refusal, c.Clarify, c.Execution, c.Semantic} {
		if chk != nil {
			return chk.Detail
		}
	}
	return ""
}

// History returns recent run summaries for the dashboard endpoint.
func History(ctx context.Context, wh *warehouse.Warehouse, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	res, err := wh.Query(ctx, fmt.Sprintf(
		`SELECT run_id,total,passed,accuracy,at FROM _nl_eval_runs ORDER BY at DESC LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, map[string]any{
			"run_id": row[0], "total": row[1], "passed": row[2], "accuracy": row[3], "at": row[4],
		})
	}
	return out, nil
}
