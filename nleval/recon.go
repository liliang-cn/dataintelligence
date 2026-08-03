package nleval

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liliang-cn/dataintelligence/engine"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/internal/strictyaml"
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

	// Value is a figure the customer already publishes, and it replaces the
	// control query rather than accompanying one.
	//
	// This is the shape an anchored case actually has. A control query written
	// to reproduce a customer's number is not evidence of anything — and if it
	// were generated from the model, it would be the compiler checking itself,
	// which passes by construction and proves nothing at all. When the customer
	// has published the number, the number *is* the control.
	Value *float64 `yaml:"value"`

	// Where restricts the metric to the scope the published figure covers.
	// "94.2%" is never the whole warehouse: it is one quarter, or one plant, or
	// both, and a case that compares it against an all-time total fails for a
	// reason that has nothing to do with the model being wrong.
	Where []semantic.Filter `yaml:"where"`

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
	Source string `yaml:"source"`
	// Tol is the tolerance. For a published value it is absolute and it is the
	// customer's own precision: "97.3%" can never prove more than three
	// decimals, and comparing it at 1e-6 fails forever against a metric that
	// agrees to every digit they have. For a control query it is relative.
	Tol float64 `yaml:"tol"`
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
	if err := strictyaml.Unmarshal(path, raw, &s); err != nil {
		return nil, err
	}
	for i, c := range s.Cases {
		switch {
		case c.Metric == "":
			return nil, fmt.Errorf("%s: case %d needs a metric", path, i+1)
		case c.Control == "" && c.Value == nil:
			return nil, fmt.Errorf("%s: case %d (%s) needs either a control query or a published value",
				path, i+1, c.Metric)
		case c.Control != "" && c.Value != nil:
			return nil, fmt.Errorf("%s: case %d (%s) has both a control query and a published value — "+
				"which one is the evidence?", path, i+1, c.Metric)
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
	Scope    string  `json:"scope,omitempty"` // what the published figure covers
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
			Source: c.Source, Anchored: c.Anchored(), Scope: scopeLabel(c.Where)}
		if res.Anchored {
			rep.Anchored++
		}
		ans, err := eng.Query(ctx, semantic.Query{Metrics: []string{c.Metric}, Where: c.Where})
		if err != nil {
			res.Error = err.Error()
			rep.Results = append(rep.Results, res)
			continue
		}
		res.Got = scalarOf(ans.Rows)
		if c.Value != nil {
			res.Want = *c.Value
		} else {
			ctrl, cerr := eng.WH.Query(ctx, c.Control)
			if cerr != nil {
				res.Error = "control query: " + cerr.Error()
				rep.Results = append(rep.Results, res)
				continue
			}
			res.Want = scalarOf(ctrl.Rows)
		}
		tol := c.Tol
		if tol <= 0 {
			tol = 1e-6
		}
		if c.Value != nil {
			res.Pass = math.Abs(res.Got-res.Want) <= tol
		} else {
			res.Pass = closeEnough(res.Got, res.Want, tol)
		}
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

// scopeLabel renders a case's restriction for a human. A report that says a
// metric matched, without saying over what, invites the reader to assume it was
// the whole warehouse.
func scopeLabel(where []semantic.Filter) string {
	if len(where) == 0 {
		return ""
	}
	parts := make([]string, 0, len(where))
	for _, f := range where {
		vals := make([]string, 0, len(f.Values))
		for _, v := range f.Values {
			vals = append(vals, fmt.Sprintf("%v", v))
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", f.Dimension, f.Op, strings.Join(vals, ",")))
	}
	return strings.Join(parts, " and ")
}

// RenderCase writes one case as the YAML an engineer pastes into the set.
//
// Printing it rather than only offering -write matters: the engineer should
// read the scope before it becomes evidence. A case appended silently is a case
// nobody checked, and this is the one file where that costs the most.
func RenderCase(c ReconCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - metric: %s\n", c.Metric)
	if c.Value != nil {
		fmt.Fprintf(&b, "    value: %s\n", strconv.FormatFloat(*c.Value, 'g', -1, 64))
	}
	if c.Control != "" {
		fmt.Fprintf(&b, "    control: %s\n", c.Control)
	}
	if len(c.Where) > 0 {
		b.WriteString("    where:\n")
		for _, f := range c.Where {
			fmt.Fprintf(&b, "      - dimension: %s\n        op: \"%s\"\n        values: [%s]\n",
				f.Dimension, f.Op, yamlValues(f.Values))
		}
	}
	if c.Tol > 0 {
		fmt.Fprintf(&b, "    tol: %s\n", strconv.FormatFloat(c.Tol, 'g', -1, 64))
	}
	if c.Source != "" {
		fmt.Fprintf(&b, "    source: %s\n", c.Source)
	}
	if c.Note != "" {
		fmt.Fprintf(&b, "    note: %q\n", c.Note)
	}
	return b.String()
}

func yamlValues(vs []any) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%q", fmt.Sprintf("%v", v)))
	}
	return strings.Join(out, ", ")
}

// AppendCase adds a case to a reconciliation set, creating it if absent.
//
// Append rather than rewrite: the file is hand-maintained, carries the notes
// that say why each definition was chosen, and rewriting it through a YAML
// round-trip would drop every comment in it — which is most of its value.
func AppendCase(path string, c ReconCase) error {
	body := RenderCase(c)
	existing, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return os.WriteFile(path, []byte("cases:\n"+body), 0o644)
	}
	if !strings.Contains(string(existing), "cases:") {
		return fmt.Errorf("%s has no `cases:` list to append to", path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		body = "\n" + body
	}
	_, err = f.WriteString(body)
	return err
}
