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
			Metrics: 8, Recon: &ReconReport{Total: 4, Passed: 4, Anchored: 2},
			Uncovered: []string{"a", "b", "c", "d"},
		}, "PARTIAL"},
		// Everything agrees and nothing was checked against anything outside the
		// model. If one person wrote both the metric and its control query from
		// the same misunderstanding, they agree — and agreement is not
		// correctness. Calling this "verified" is the lie the report exists to
		// avoid.
		{"all agree, none anchored", Delivery{
			Metrics: 4, Recon: &ReconReport{Total: 4, Passed: 4, Anchored: 0},
		}, "SELF-CONSISTENT"},
		{"everything covered, passing and anchored", Delivery{
			Metrics: 4, Recon: &ReconReport{Total: 4, Passed: 4, Anchored: 3},
		}, "VERIFIED"},
		// Two gaps at once: neither may hide the other.
		{"unchecked and unanchored", Delivery{
			Metrics: 3, Recon: &ReconReport{Total: 1, Passed: 1, Anchored: 0},
			Uncovered: []string{"a", "b"},
		}, "PARTIAL"},
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

// A reader has to be able to tell, per metric, whether the expected figure came
// from the customer or from the engineer who wrote the metric.
func TestReportNamesTheAnchorPerMetric(t *testing.T) {
	d := Delivery{
		Database: "acme", Model: "m.yaml", Metrics: 2,
		Recon: &ReconReport{Total: 2, Passed: 2, Anchored: 1, Results: []ReconResult{
			{Metric: "revenue", Got: 10, Want: 10, Pass: true, Source: SourceCustomerReport, Anchored: true},
			{Metric: "orders", Got: 5, Want: 5, Pass: true, Source: SourceEngineer},
		}},
	}
	var b strings.Builder
	d.WriteMarkdown(&b)
	out := b.String()

	for _, want := range []string{"customer report", "*derived*", "1 of 2 controls have no external anchor"} {
		if !strings.Contains(out, want) {
			t.Errorf("report should contain %q:\n%s", want, out)
		}
	}
}

func TestAnchorLabel(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{SourceCustomerReport, "customer report"},
		{SourceCustomerSystem, "customer system"},
		{SourceEngineer, "*derived*"},
		// An unrecorded source is not the same as a derived one, and must not
		// be presented as either an anchor or a derivation.
		{"", "*unrecorded*"},
		{"the finance team's spreadsheet", "the finance team's spreadsheet"},
	} {
		if got := anchorLabel(ReconResult{Source: tc.src}); got != tc.want {
			t.Errorf("anchorLabel(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}
