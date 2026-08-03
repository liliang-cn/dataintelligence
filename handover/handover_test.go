package handover

import (
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/dataintelligence/engagement"
)

// A monitor that says only "drift detected" hands the reader a search problem
// at the worst possible moment.
func TestDriftNamesWhatBrokeAndWhatItMeans(t *testing.T) {
	d := &Drift{
		MissingTables:  []string{"legacy_orders"},
		MissingColumns: []ColumnRef{{Table: "stores", Column: "city", UsedBy: "store_city"}},
		StaleFeeds:     []StaleFeed{{Dimension: "synced_at", Newest: "2025-12-01", Days: 245}},
		Failing:        []FailedCase{{Metric: "order_count", Got: 2001, Want: 2000, Source: "customer-report"}},
	}
	if d.Clean() {
		t.Fatal("this is not clean")
	}
	var b strings.Builder
	d.WriteText(&b)
	out := b.String()

	for _, want := range []string{
		"legacy_orders", "renamed",
		"stores.city", "store_city",
		"245 days ago", "no longer growing",
		"order_count = 2001", "2000", "customer-report",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drift output should mention %q:\n%s", want, out)
		}
	}
}

// A metric that stopped matching a figure the customer publishes is a different
// conversation from one that stopped matching the engineer's own derivation.
func TestDriftDistinguishesAnchoredFailures(t *testing.T) {
	anchored := &Drift{Failing: []FailedCase{{Metric: "revenue", Got: 1, Want: 2, Source: "customer-report"}}}
	derived := &Drift{Failing: []FailedCase{{Metric: "revenue", Got: 1, Want: 2, Source: "engineer"}}}

	var a, e strings.Builder
	anchored.WriteText(&a)
	derived.WriteText(&e)
	if !strings.Contains(a.String(), "anchored to") {
		t.Error("an anchored failure should say so")
	}
	if strings.Contains(e.String(), "anchored to") {
		t.Error("a derived control is not an anchor")
	}
}

func TestCleanDriftSaysSoPlainly(t *testing.T) {
	d := &Drift{}
	if !d.Clean() || !strings.Contains(d.Summary(), "no drift") {
		t.Errorf("summary = %q", d.Summary())
	}
}

// The signal is repetition: one customer wanting fiscal years starting in April
// is a workaround, three is a missing feature.
func TestRollupRanksByHowOftenAGapRecurs(t *testing.T) {
	mk := func(customer string, items ...engagement.DeltaItem) *engagement.Engagement {
		return &engagement.Engagement{Customer: customer, Delta: items}
	}
	fiscal := engagement.DeltaItem{Kind: "missing-feature", What: "fiscal year starts in April"}
	r := Rollup([]*engagement.Engagement{
		mk("A", fiscal, engagement.DeltaItem{Kind: "transform", What: "amounts stored in cents"}),
		mk("B", fiscal),
		// Same gap, described with different spacing and case — normalised.
		mk("C", engagement.DeltaItem{Kind: "Missing-Feature", What: "  Fiscal year   starts in April "}),
	})

	if len(r.Items) != 2 {
		t.Fatalf("want 2 distinct gaps, got %d: %+v", len(r.Items), r.Items)
	}
	top := r.Items[0]
	if top.Count != 3 {
		t.Errorf("the repeated gap should count 3, got %d", top.Count)
	}
	if len(top.Customers) != 3 {
		t.Errorf("customers = %v", top.Customers)
	}
	if !strings.Contains(r.Summary(), "1 hit more than once") {
		t.Errorf("summary = %q", r.Summary())
	}
}

// The same customer hitting a gap twice is still one customer: counting it
// twice would promote a single deployment's quirk to a product requirement.
func TestRollupCountsCustomersNotOccurrences(t *testing.T) {
	same := engagement.DeltaItem{Kind: "manual", What: "monthly Excel export"}
	r := Rollup([]*engagement.Engagement{
		{Customer: "A", Delta: []engagement.DeltaItem{same, same}},
	})
	if r.Items[0].Count != 1 {
		t.Errorf("count = %d, want 1", r.Items[0].Count)
	}
}

// An empty rollup is a finding about the process, not an empty page.
func TestEmptyRollupSaysWhyThatIsSuspicious(t *testing.T) {
	var b strings.Builder
	Rollup(nil).WriteMarkdown(&b)
	if !strings.Contains(b.String(), "not being filled in") {
		t.Errorf("an empty rollup should question itself:\n%s", b.String())
	}
}

// The negative is the valuable half: a correct dashboard nobody opens is worth
// the same as a wrong one.
func TestAdoptionLeadsWithNobodyAsking(t *testing.T) {
	var b strings.Builder
	(&Adoption{Days: 30}).WriteMarkdown(&b)
	out := b.String()
	if !strings.Contains(out, "Nobody asked anything") {
		t.Errorf("zero usage should be stated first:\n%s", out)
	}

	a := &Adoption{
		Days: 30, Queries: 12,
		Users:   []UserUsage{{User: "ceo", Role: "ceo", Queries: 12}},
		Metrics: []MetricUsage{{Metric: "revenue", Queries: 12}},
		Unused:  []string{"churn", "margin"},
	}
	b.Reset()
	a.WriteMarkdown(&b)
	if !strings.Contains(b.String(), "never asked for (2)") {
		t.Errorf("unused metrics should be called out:\n%s", b.String())
	}
	if !strings.Contains(a.Summary(), "2 metric(s) never asked for") {
		t.Errorf("summary = %q", a.Summary())
	}
}

// now() - interval is Postgres-only, and on another engine it returns
// everything rather than erroring — a window silently ignored.
func TestSinceExprPerEngine(t *testing.T) {
	for driver, want := range map[string]string{
		"pgx":       "interval '7 days'",
		"mysql":     "INTERVAL 7 DAY",
		"sqlite":    "'-7 days'",
		"sqlserver": "DATEADD(day, -7",
	} {
		if got := sinceExpr(driver, 7); !strings.Contains(got, want) {
			t.Errorf("sinceExpr(%q) = %q, want it to contain %q", driver, got, want)
		}
	}
}

func TestSplitMetricsReadsTheTrailFormat(t *testing.T) {
	for in, want := range map[string]int{
		"[revenue order_count]": 2,
		"[revenue]":             1,
		"[]":                    0,
		"":                      0,
	} {
		if got := len(splitMetrics(in)); got != want {
			t.Errorf("splitMetrics(%q) = %d, want %d", in, got, want)
		}
	}
}

// The workflow has to be usable without editing: the DSN variables become
// repository secrets of the same name.
func TestWorkflowWiresSecretsFromTheEngagement(t *testing.T) {
	e := &engagement.Engagement{
		Customer:  "Acme",
		Databases: []engagement.Database{{ID: "erp", Vars: []string{"ACME_ERP_DSN"}}},
	}
	y := (&Runbook{E: e}).WorkflowYAML()
	if !strings.Contains(y, "ACME_ERP_DSN: ${{ secrets.ACME_ERP_DSN }}") {
		t.Errorf("workflow should wire the secret:\n%s", y)
	}
	for _, want := range []string{"di eval", "di drift", "schedule:"} {
		if !strings.Contains(y, want) {
			t.Errorf("workflow should run %q", want)
		}
	}
}

func TestRunbookTellsThemWhatIsTheirsToDecide(t *testing.T) {
	e := &engagement.Engagement{
		Customer: "Acme", Owner: "liliang",
		Databases: []engagement.Database{
			{ID: "erp", Model: "models/erp.yaml", Recon: "models/erp.recon.yaml"},
			{ID: "pos"}, // unmodelled — the runbook must not pretend otherwise
		},
		Deliver: engagement.Deliver{Roles: []string{"ceo", "finance"}},
		Delta:   []engagement.DeltaItem{{Kind: "manual", What: "monthly export", Workaround: "sql/x.sql"}},
	}
	var b strings.Builder
	(&Runbook{E: e}).WriteMarkdown(&b)
	out := b.String()

	for _, want := range []string{
		"1 of them modelled",
		"governed", "unmodelled",
		"What a metric means", // theirs, not the tool's
		"Known rough edges", "monthly export",
		"di drift", "source",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("runbook should contain %q:\n%s", want, out)
		}
	}
}

func TestAsTimeAcceptsWhatDriversActuallyReturn(t *testing.T) {
	want := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, v := range []any{
		want,
		"2025-12-01",
		"2025-12-01 00:00:00",
		[]byte("2025-12-01T00:00:00Z"),
	} {
		got, ok := asTime(v)
		if !ok || !got.Equal(want) {
			t.Errorf("asTime(%v) = %v, %v", v, got, ok)
		}
	}
	if _, ok := asTime(42); ok {
		t.Error("a number is not a timestamp")
	}
}

// The Day 2 check must group a shared cutoff the same way the survey does —
// and it must use the survey's threshold rather than its own copy, since two
// copies of "is this stale" drift apart and the one running every morning is
// the one whose false alarms teach people to ignore it.
func TestDriftGroupsFeedsThatStoppedTogether(t *testing.T) {
	d := &Drift{StaleFeeds: []StaleFeed{
		{Dimension: "sale_month", Newest: "2025-12-01", Days: 245},
		{Dimension: "shrinkage_month", Newest: "2025-12-01", Days: 245},
		{Dimension: "overhead_month", Newest: "2025-12-01", Days: 245},
		{Dimension: "employee_hire_date", Newest: "2023-12-05", Days: 972},
	}}
	if !strings.Contains(d.Summary(), "3 feeds all stopped on 2025-12-01") {
		t.Errorf("summary should group the shared cutoff: %q", d.Summary())
	}

	var b strings.Builder
	d.WriteText(&b)
	out := b.String()
	// It appears in the summary line and once in the detail, not once per feed.
	for _, grouped := range []string{"sale_month stops at", "shrinkage_month stops at", "overhead_month stops at"} {
		if strings.Contains(out, grouped) {
			t.Errorf("%q should have been folded into the shared-cutoff line:\n%s", grouped, out)
		}
	}
	// The unrelated one still gets its own line.
	if !strings.Contains(out, "employee_hire_date") {
		t.Errorf("an unrelated stale feed should still be listed:\n%s", out)
	}
}

// A format verb split across two writes prints "%!s(MISSING)" into the document
// the customer is handed. Render the whole runbook and check it came out clean.
func TestRunbookHasNoFormattingLeftovers(t *testing.T) {
	e := &engagement.Engagement{
		Customer:  "Acme",
		Databases: []engagement.Database{{ID: "erp", Model: "m.yaml", Recon: "m.recon.yaml"}},
		Deliver:   engagement.Deliver{Roles: []string{"ceo", "finance"}},
	}
	var b strings.Builder
	(&Runbook{E: e}).WriteMarkdown(&b)
	out := b.String()

	for _, bad := range []string{"%!", "(MISSING)", "%s", "%d"} {
		if strings.Contains(out, bad) {
			t.Errorf("unrendered format verb %q in the runbook:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "ceo, finance") {
		t.Errorf("the roles should appear in the sentence:\n%s", out)
	}
}
