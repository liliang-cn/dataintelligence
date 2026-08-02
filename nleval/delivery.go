package nleval

import (
	"fmt"
	"io"
	"sort"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// Delivery is what an engineer hands over at the end of an engagement.
//
// Modelling a customer's warehouse is otherwise unfalsifiable work: you deliver
// a YAML file and a dashboard, and nobody — including you — can say whether the
// numbers are right. Both gates that answer that question already run; nothing
// rendered their output as one document for the person paying for it.
//
// It is deliberately unflattering. Uncovered metrics and failed cases appear
// first and by name, because a report that only shows what passed is marketing,
// and the reader will discover the gaps later anyway — at a worse moment.
type Delivery struct {
	Database string   `json:"database"`
	Model    string   `json:"model"`
	Entities int      `json:"entities"`
	Joins    int      `json:"joins"`
	Dims     int      `json:"dimensions"`
	Metrics  int      `json:"metrics"`
	Roles    []string `json:"roles,omitempty"`
	Masked   []string `json:"masked_dimensions,omitempty"`

	Recon     *ReconReport `json:"reconciliation,omitempty"`
	Uncovered []string     `json:"uncovered_metrics,omitempty"`
	NL        *Report      `json:"nl_accuracy,omitempty"`

	Notes []string `json:"notes,omitempty"` // model lint notes
}

// Describe fills in the shape of the model: what was actually built.
func (d *Delivery) Describe(m *semantic.Model) {
	d.Entities, d.Joins = len(m.Entities), len(m.Joins)
	d.Dims, d.Metrics = len(m.Dimensions), len(m.Metrics)

	roles := map[string]bool{}
	for i := range m.Metrics {
		for _, r := range m.Metrics[i].Roles {
			roles[r] = true
		}
	}
	for r := range roles {
		d.Roles = append(d.Roles, r)
	}
	sort.Strings(d.Roles)

	for i := range m.Dimensions {
		if m.Dimensions[i].Mask != "" {
			d.Masked = append(d.Masked, m.Dimensions[i].Name)
		}
	}
	sort.Strings(d.Masked)
}

// Verdict is the one line someone reads first.
//
// It is "not verified" when no metric was reconciled, rather than a vacuous
// 0/0 pass. A model nobody checked and a model that checked out must not print
// the same word.
func (d *Delivery) Verdict() string {
	switch {
	case d.Recon == nil || d.Recon.Total == 0:
		return "NOT VERIFIED — no metric has a control query"
	case d.Recon.Passed < d.Recon.Total:
		return fmt.Sprintf("FAILING — %d of %d metrics disagree with their control query",
			d.Recon.Total-d.Recon.Passed, d.Recon.Total)
	case len(d.Uncovered) > 0:
		return fmt.Sprintf("PARTIAL — %d/%d metrics reconcile, %d have no control query",
			d.Recon.Passed, d.Metrics, len(d.Uncovered))
	default:
		return fmt.Sprintf("VERIFIED — all %d metrics reconcile with a hand-written control query",
			d.Recon.Passed)
	}
}

// WriteMarkdown renders the handover document.
func (d *Delivery) WriteMarkdown(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("# Delivery report — %s", orDash(d.Database))
	p("")
	p("**%s**", d.Verdict())
	p("")
	p("| | |")
	p("|---|---|")
	p("| Semantic model | `%s` |", d.Model)
	p("| Entities · joins | %d · %d |", d.Entities, d.Joins)
	p("| Dimensions · metrics | %d · %d |", d.Dims, d.Metrics)
	if len(d.Roles) > 0 {
		p("| Roles gating metrics | %s |", strings.Join(d.Roles, ", "))
	}
	if len(d.Masked) > 0 {
		p("| Masked dimensions | %s |", strings.Join(d.Masked, ", "))
	}

	// Gaps first. A reader who stops after the summary should still have seen
	// what is missing.
	if len(d.Uncovered) > 0 {
		p("")
		p("## Not verified (%d)", len(d.Uncovered))
		p("")
		p("These metrics compile, and nothing checks that they are right. Each needs a")
		p("control query written by someone who knows the business:")
		p("")
		for _, m := range d.Uncovered {
			p("- `%s`", m)
		}
	}

	if d.Recon != nil && d.Recon.Total > 0 {
		p("")
		p("## Metric reconciliation — %d/%d", d.Recon.Passed, d.Recon.Total)
		p("")
		p("Every metric computed through the semantic layer, then again with a control")
		p("query written by hand. They must agree.")
		p("")
		p("| Metric | Semantic layer | Control | |")
		p("|---|---:|---:|---|")
		for _, r := range d.Recon.Results {
			switch {
			case r.Error != "":
				p("| `%s` | — | — | ✗ %s |", r.Metric, oneLine(r.Error))
			case r.Pass:
				p("| `%s` | %s | %s | ✓ |", r.Metric, Num(r.Got), Num(r.Want))
			default:
				p("| `%s` | %s | %s | **✗ differs** |", r.Metric, Num(r.Got), Num(r.Want))
			}
		}
		for _, r := range d.Recon.Results {
			if r.Note != "" {
				p("")
				p("- **%s** — %s", r.Metric, r.Note)
			}
		}
	}

	if d.NL != nil && d.NL.Total > 0 {
		p("")
		p("## Natural-language accuracy — %.0f%% (%d/%d)",
			d.NL.Acc*100, d.NL.Passed, d.NL.Total)
		p("")
		p("Labelled questions asked end to end: grounded, governed, executed, and the")
		p("numbers compared with the control.")
		p("")
		p("| Category | Accuracy | |")
		p("|---|---:|---|")
		for _, c := range d.NL.Categories {
			p("| %s | %.0f%% | %d/%d |", c.Category, c.Acc*100, c.Passed, c.Total)
		}
		if d.NL.Skipped > 0 {
			p("")
			p("%d case(s) skipped — they need an LLM and none was configured.", d.NL.Skipped)
		}
		p("")
		p("Latency p50 %dms · p95 %dms · max %dms.", d.NL.P50Ms, d.NL.P95Ms, d.NL.MaxMs)

		var failed []string
		for _, c := range d.NL.Cases {
			if !c.Skipped && !c.Passed {
				failed = append(failed, fmt.Sprintf("- **%s** — %q → got %v, expected %v",
					c.Case, c.Question, c.Predicted, c.Expected))
			}
		}
		if len(failed) > 0 {
			p("")
			p("### Questions still answered wrong (%d)", len(failed))
			p("")
			for _, f := range failed {
				p("%s", f)
			}
		}
	}

	if len(d.Notes) > 0 {
		p("")
		p("## Model lint")
		p("")
		for _, n := range d.Notes {
			p("- %s", n)
		}
	}

	p("")
	p("---")
	p("")
	p("Reproduce: `di report -model %s -dsn <dsn>`", d.Model)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Num prints a figure the way a reader compares two of them: no exponent, no
// trailing noise, and integers without a decimal point.
func Num(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}
