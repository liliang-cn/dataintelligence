package nleval

import (
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

// A model nobody checked and a model that checked out must not print the same
// word. "0/0 passed" would technically be true and would read as verification.
func TestVerdictDistinguishesUncheckedFromVerified(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Delivery
		want string
	}{
		{"no control queries at all", Delivery{Metrics: 8}, "NOT VERIFIED"},
		{"empty recon set", Delivery{Metrics: 8, Recon: &ReconReport{}}, "NOT VERIFIED"},
		{"a metric disagrees", Delivery{
			Metrics: 3, Recon: &ReconReport{Total: 3, Passed: 2},
		}, "FAILING"},
		{"all checked pass, some unchecked", Delivery{
			Metrics: 8, Recon: &ReconReport{Total: 4, Passed: 4},
			Uncovered: []string{"a", "b", "c", "d"},
		}, "PARTIAL"},
		{"everything covered and passing", Delivery{
			Metrics: 4, Recon: &ReconReport{Total: 4, Passed: 4},
		}, "VERIFIED"},
	} {
		if got := tc.d.Verdict(); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: verdict = %q, want it to start with %q", tc.name, got, tc.want)
		}
	}
}

// The gaps are the part a reader will otherwise discover later, at a worse
// moment. They come before the passing table, and every one is named.
func TestReportNamesWhatIsUnverifiedBeforeWhatPassed(t *testing.T) {
	d := Delivery{
		Database: "acme", Model: "m.yaml", Metrics: 3,
		Recon:     &ReconReport{Total: 1, Passed: 1, Results: []ReconResult{{Metric: "revenue", Got: 10, Want: 10, Pass: true}}},
		Uncovered: []string{"margin", "churn"},
	}
	var b strings.Builder
	d.WriteMarkdown(&b)
	out := b.String()

	for _, want := range []string{"acme", "Not verified (2)", "`margin`", "`churn`", "PARTIAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("report should mention %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Not verified") > strings.Index(out, "Metric reconciliation") {
		t.Error("the gaps must appear before the passing table")
	}
}

func TestDescribeReadsGovernanceOffTheModel(t *testing.T) {
	m := &semantic.Model{
		Entities:   []semantic.Entity{{Name: "order", Table: "orders", PrimaryKey: "id"}},
		Dimensions: []semantic.Dimension{{Name: "email", Entity: "order", Column: "email", Mask: "hash"}},
		Metrics: []semantic.Metric{
			{Name: "revenue", Description: "d", Entity: "order", Agg: "sum", Expr: "amount", Roles: []string{"finance"}},
			{Name: "count", Description: "d", Entity: "order", Agg: "count", Expr: "id"},
		},
	}
	var d Delivery
	d.Describe(m)
	if d.Metrics != 2 || d.Dims != 1 || d.Entities != 1 {
		t.Errorf("shape wrong: %+v", d)
	}
	// Who is gated and what is hidden are the two governance facts a customer
	// asks about; both are read off the model rather than asserted in prose.
	if len(d.Roles) != 1 || d.Roles[0] != "finance" {
		t.Errorf("roles = %v, want [finance]", d.Roles)
	}
	if len(d.Masked) != 1 || d.Masked[0] != "email" {
		t.Errorf("masked = %v, want [email]", d.Masked)
	}
}

func TestNumIsReadableNextToAnotherNumber(t *testing.T) {
	// 6.17874362e+06 and 6178743.62 are the same figure; only one can be
	// compared with the number beside it at a glance.
	for in, want := range map[float64]string{
		6178743.62: "6178743.62",
		36210:      "36210",
		0:          "0",
		1235.7487:  "1235.7487",
	} {
		if got := Num(in); got != want {
			t.Errorf("Num(%v) = %q, want %q", in, got, want)
		}
	}
}
