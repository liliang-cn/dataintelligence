package nleval

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/dataintelligence/engine"
	semantic "github.com/liliang-cn/semantic-go"
	"gopkg.in/yaml.v3"
)

// ReconCase pins one metric to a hand-written control query.
//
// This is the check that catches the failure this whole layer exists to
// prevent: the compiler produced a number, cleanly, and it is the wrong number.
// A control query written by a person who knows the business is the only thing
// that can say so — the compiler cannot check itself.
type ReconCase struct {
	Metric  string `yaml:"metric"`
	Control string `yaml:"control"` // SQL returning one scalar
	Note    string `yaml:"note"`    // why this definition, in the customer's words

	// Source is where the expected number came from. It is the difference
	// between verification and theatre.
	//
	// If the control query is written by whoever wrote the metric, from the same
	// misunderstanding, the two agree — and agreement is not correctness. A
	// control anchored to a figure the customer already publishes is evidence;
	// one the engineer derived from the same schema is a consistency check.
	// Both are worth having. Reporting them as the same thing is not.
	//
	//   customer-report   a number the customer publishes (name it in note)
	//   customer-system   read from another system they trust
	//   engineer          derived from the schema — no external anchor
	Source string  `yaml:"source"`
	Tol    float64 `yaml:"tol"` // relative tolerance; 0 → 1e-6
}

// Anchored reports whether this case is tied to something outside the model.
func (c ReconCase) Anchored() bool {
	return c.Source != "" && c.Source != SourceEngineer
}

// Recognised sources. Anything else is treated as an anchor, since a value
// nobody recognised is more likely a customer system than a slip.
const (
	SourceCustomerReport = "customer-report"
	SourceCustomerSystem = "customer-system"
	SourceEngineer       = "engineer"
)

// ReconSet is the reconciliation contract for one semantic model.
//
// It lives beside the model as data, not in Go. The cases were hardcoded to one
// example's metrics, which meant the gate only ever validated that example: an
// engineer standing up a new customer got a green check for metrics that
// customer does not have. A reconciliation set that does not describe the model
// in front of you is worse than none, because it reads as verification.
type ReconSet struct {
	Cases []ReconCase `yaml:"cases"`
}

// LoadReconSet reads a reconciliation set. The conventional location is beside
// the model: models/shop.yaml → models/shop.recon.yaml.
func LoadReconSet(path string) (*ReconSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s ReconSet
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, c := range s.Cases {
		if c.Metric == "" || c.Control == "" {
			return nil, fmt.Errorf("%s: case %d needs both a metric and a control query", path, i+1)
		}
	}
	return &s, nil
}

// ReconPathFor is the conventional reconciliation-set path for a model.
func ReconPathFor(modelPath string) string {
	ext := filepath.Ext(modelPath)
	return strings.TrimSuffix(modelPath, ext) + ".recon" + ext
}

// ReconResult is one metric's outcome.
type ReconResult struct {
	Metric   string  `json:"metric"`
	Control  string  `json:"control"`
	Note     string  `json:"note,omitempty"`
	Source   string  `json:"source,omitempty"`
	Anchored bool    `json:"anchored"`
	Got      float64 `json:"got"`
	Want     float64 `json:"want"`
	Pass     bool    `json:"pass"`
	Error    string  `json:"error,omitempty"`
}

// ReconReport is the whole reconciliation run.
type ReconReport struct {
	Results  []ReconResult `json:"results"`
	Total    int           `json:"total"`
	Passed   int           `json:"passed"`
	Declared int           `json:"declared_metrics"` // metrics in the model
	Covered  int           `json:"covered_metrics"`  // metrics with a control query
	Anchored int           `json:"anchored"`         // covered by a customer figure, not a derivation
}

// Uncovered lists model metrics with no control query. Coverage is reported
// rather than assumed: eleven passing checks over forty metrics is a different
// statement than eleven over eleven, and only one of them is worth showing a
// customer.
func (r *ReconReport) Uncovered(m *semantic.Model) []string {
	have := map[string]bool{}
	for _, res := range r.Results {
		have[res.Metric] = true
	}
	var out []string
	for i := range m.Metrics {
		if !have[m.Metrics[i].Name] {
			out = append(out, m.Metrics[i].Name)
		}
	}
	return out
}

// Reconcile runs every case: compute the metric through the semantic layer,
// run the control query directly, compare.
func Reconcile(ctx context.Context, eng *engine.Engine, set *ReconSet) (*ReconReport, error) {
	if eng.Model == nil {
		return nil, fmt.Errorf("reconciliation needs a semantic model")
	}
	rep := &ReconReport{Declared: len(eng.Model.Metrics)}
	for _, c := range set.Cases {
		res := ReconResult{Metric: c.Metric, Control: c.Control, Note: c.Note,
			Source: c.Source, Anchored: c.Anchored()}
		if res.Anchored {
			rep.Anchored++
		}
		ans, err := eng.Query(ctx, semantic.Query{Metrics: []string{c.Metric}})
		if err != nil {
			res.Error = err.Error()
			rep.Results = append(rep.Results, res)
			continue
		}
		ctrl, err := eng.WH.Query(ctx, c.Control)
		if err != nil {
			res.Error = "control query: " + err.Error()
			rep.Results = append(rep.Results, res)
			continue
		}
		res.Got, res.Want = scalarOf(ans.Rows), scalarOf(ctrl.Rows)
		tol := c.Tol
		if tol <= 0 {
			tol = 1e-6
		}
		res.Pass = closeEnough(res.Got, res.Want, tol)
		if res.Pass {
			rep.Passed++
		}
		rep.Results = append(rep.Results, res)
	}
	rep.Total = len(rep.Results)
	rep.Covered = rep.Total
	return rep, nil
}

func scalarOf(rows [][]any) float64 {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return 0
	}
	switch v := rows[0][0].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		var f float64
		_, _ = fmt.Sscan(string(v), &f)
		return f
	case string:
		var f float64
		_, _ = fmt.Sscan(v, &f)
		return f
	default:
		return 0
	}
}

func closeEnough(a, b, tol float64) bool {
	if a == b {
		return true
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return true
	}
	return math.Abs(a-b)/scale <= tol
}
