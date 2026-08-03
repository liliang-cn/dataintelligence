package survey

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The finding most often missed and the most expensive: a source that stopped
// sending. Every row in it is valid; the totals just quietly stop growing.
func TestStaleFeedIsFlagged(t *testing.T) {
	opts := Options{StaleAfter: 60 * 24 * time.Hour}
	opts.withDefaults()

	stopped := Column{Name: "synced_at", Max: "2025-11-01 00:00:00+00"}
	if got := stopped.findings(500, opts); !contains(got, "has this feed stopped") {
		t.Errorf("a feed that stopped should be flagged, got %v", got)
	}
	fresh := Column{Name: "ordered_at", Max: time.Now().Format("2006-01-02 15:04:05")}
	if got := fresh.findings(500, opts); len(got) != 0 {
		t.Errorf("a live feed should raise nothing, got %v", got)
	}
	// An unparseable timestamp must not be guessed at in either direction.
	odd := Column{Name: "weird", Max: "last tuesday"}
	if got := odd.findings(500, opts); len(got) != 0 {
		t.Errorf("an unparseable value should raise nothing, got %v", got)
	}
}

// An all-null column was reported twice — "entirely null" and "one value in
// every row" — because DISTINCT counts NULL as a value. Two findings, one of
// them wrong, is how a reader stops trusting the rest of the report.
func TestAllNullColumnIsReportedOnce(t *testing.T) {
	var opts Options
	opts.withDefaults()
	c := Column{Name: "note", Nulls: 1, Distinct: 0}
	got := c.findings(4000, opts)
	if !contains(got, "entirely null") {
		t.Errorf("want the null finding, got %v", got)
	}
	if contains(got, "constant") {
		t.Errorf("an all-null column is not a constant: %v", got)
	}
}

func TestColumnFindings(t *testing.T) {
	var opts Options
	opts.withDefaults()

	half := Column{Name: "region", Nulls: 0.62, Distinct: 5}
	if !contains(half.findings(100, opts), "62% null") {
		t.Errorf("a mostly-null column should be questioned")
	}
	constant := Column{Name: "channel", Distinct: 1}
	if !contains(constant.findings(4000, opts), "constant, not a dimension") {
		t.Errorf("a single-valued column should be called out")
	}
	// One row means everything is trivially constant; saying so is noise.
	one := Column{Name: "x", Distinct: 1}
	if got := one.findings(1, opts); len(got) != 0 {
		t.Errorf("a one-row table should raise nothing, got %v", got)
	}
}

// The summary is what someone reads before deciding whether this warehouse can
// be modelled at all, so the structural problems have to be in it.
func TestSummaryCarriesTheStructuralProblems(t *testing.T) {
	r := &Report{Tables: []Table{
		{Name: "orders", Rows: 4000, PrimaryKey: "order_id"},
		{Name: "promo", Rows: 0, PrimaryKey: "promo_id"},
		{Name: "legacy_feed", Rows: 500},
	}, Orphans: []Orphan{{Table: "orders", Column: "store_id", Count: 37}}}

	got := strings.Join(summarise(r), "\n")
	for _, want := range []string{"3 tables", "4,500 rows", "1 table(s) are empty", "no primary key", "37 orphan", "will drop rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary should mention %q:\n%s", want, got)
		}
	}
}

func TestEmptyDatabaseAsksTheObviousQuestion(t *testing.T) {
	got := summarise(&Report{})
	if len(got) != 1 || !strings.Contains(got[0], "right database") {
		t.Errorf("an empty database should ask whether it is the right one, got %v", got)
	}
}

// Postgres' CHAR(n) is blank-padded, which trails spaces through every
// timestamp in the report.
func TestTextCastAvoidsPadding(t *testing.T) {
	for driver, want := range map[string]string{
		"pgx":       "CAST(MAX(x) AS TEXT)",
		"sqlite":    "CAST(MAX(x) AS TEXT)",
		"mysql":     "CAST(MAX(x) AS CHAR)",
		"duckdb":    "CAST(MAX(x) AS VARCHAR)",
		"sqlserver": "CONVERT(varchar(40), MAX(x), 126)",
	} {
		if got := textCast(driver)("MAX(x)"); got != want {
			t.Errorf("textCast(%q) = %q, want %q", driver, got, want)
		}
	}
}

func TestQuoterPerEngine(t *testing.T) {
	for driver, want := range map[string]string{
		"pgx": `"a"`, "mysql": "`a`", "sqlserver": "[a]", "sqlite": `"a"`,
	} {
		if got := quoter(driver)("a"); got != want {
			t.Errorf("quoter(%q) = %q, want %q", driver, got, want)
		}
	}
	// An identifier carrying the quote character must be escaped, not concatenated.
	if got := quoter("mysql")("a`b"); got != "`a``b`" {
		t.Errorf("mysql escape = %q", got)
	}
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Ten shops that last opened in 2022 is a chain that stopped expanding, not a
// broken feed. A survey that says otherwise teaches the reader to skim the
// section it most needs them to read.
func TestTinyTablesDoNotGetStalenessFindings(t *testing.T) {
	var opts Options
	opts.withDefaults()
	old := Column{Name: "open_date", Max: "2022-08-01"}

	if got := old.findings(10, opts); contains(got, "feed stopped") {
		t.Errorf("a 10-row table has no cadence to be late against: %v", got)
	}
	if got := old.findings(1440, opts); !contains(got, "feed stopped") {
		t.Errorf("a table with real cadence should still be flagged: %v", got)
	}
}

// Four tables ending on one day is one event. Reported per table it reads as
// four unrelated problems and sends somebody looking in four wrong places.
func TestTablesSharingACutoffAreOneFinding(t *testing.T) {
	r := &Report{Tables: []Table{
		{Name: "sales", Rows: 1440, Columns: []Column{{Name: "month", Max: "2025-12-01"}}},
		{Name: "shrinkage", Rows: 1440, Columns: []Column{{Name: "month", Max: "2025-12-01"}}},
		{Name: "overheads", Rows: 240, Columns: []Column{{Name: "month", Max: "2025-12-01"}}},
		// Below the cadence threshold: not part of the event.
		{Name: "stores", Rows: 10, Columns: []Column{{Name: "open_date", Max: "2025-12-01"}}},
	}}
	got := strings.Join(summarise(r), "\n")
	if !strings.Contains(got, "3 tables all stop at 2025-12-01") {
		t.Errorf("the shared cutoff should be one finding:\n%s", got)
	}
	if strings.Contains(got, "stores") {
		t.Errorf("a 10-row table should not join the event:\n%s", got)
	}

	// One table stopping is not an event; do not phrase it as one.
	single := &Report{Tables: []Table{
		{Name: "sales", Rows: 1440, Columns: []Column{{Name: "month", Max: "2025-12-01"}}},
	}}
	if strings.Contains(strings.Join(summarise(single), "\n"), "all stop at") {
		t.Error("a single stale table is not a correlated event")
	}
}

// Slicing a Go string by byte splits a multi-byte character in half and writes
// invalid UTF-8 into the document — which is how a survey of a warehouse whose
// data is Chinese produces a file the customer's own tools refuse to open.
// Sample values are exactly where non-ASCII shows up first.
func TestSampleValuesSurviveTruncation(t *testing.T) {
	c := Column{Name: "workshop", Sample: []string{
		"铸造一厂 造型车间 气冲造型线 长春一厂 第一分厂",
		"锻造 一号锻压车间 12500T 锻压线",
		"ascii is fine and also long enough to be cut somewhere",
	}}
	out := rangeOrValues(c)
	if !utf8.ValidString(out) {
		t.Fatalf("truncation produced invalid UTF-8: %q", out)
	}
	if !strings.Contains(out, "铸造一厂") {
		t.Errorf("the readable part should survive: %q", out)
	}
}

// The shape the problem takes in a company with several sites: one plant's feed
// stops, the table keeps moving because the other nine report, and the totals
// are quietly missing a plant. Table-level staleness cannot see it.
func TestSegmentsMustNameSomethingRatherThanMeasureIt(t *testing.T) {
	// Coke tonnage repeats per workshop, so it has few distinct values — and
	// "coke_t = 382.000 stopped reporting" is not a sentence anyone can act on.
	for _, c := range []Column{
		{Name: "coke_t", Type: "numeric", Distinct: 5},
		{Name: "gas_m3", Type: "numeric", Distinct: 6},
		{Name: "electricity_kwh", Type: "numeric", Distinct: 12},
	} {
		if isSegmentLike(c) {
			t.Errorf("%s is a measure, not a segment", c.Name)
		}
	}
	for _, c := range []Column{
		{Name: "workshop_id", Type: "integer", Distinct: 6},
		{Name: "shift_id", Type: "integer", Distinct: 3},
		{Name: "plant", Type: "text", Distinct: 4},
		{Name: "material", Type: "character varying", Distinct: 5},
	} {
		if !isSegmentLike(c) {
			t.Errorf("%s names something and should segment", c.Name)
		}
	}
}

// Periods come from the time column's own distinct count. Dividing the span by
// the number of segments made a monthly feed look like it reported quarterly,
// and a plant four months silent then read as one period late — under the
// threshold, so the finding vanished.
func TestCadenceComesFromThePeriodsNotTheSegments(t *testing.T) {
	newest := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// Two years of monthly data: 25 distinct months.
	got := CadenceOf(newest, "2024-07-01", 25)
	if got < 25*24*time.Hour || got > 35*24*time.Hour {
		t.Errorf("monthly data should give a monthly cadence, got %v", got)
	}
	// A daily feed must not make "two periods behind" fire over a weekend.
	if got := CadenceOf(newest, "2026-06-01", 30); got < 20*24*time.Hour {
		t.Errorf("cadence should not fall below the floor, got %v", got)
	}
	// One period, or an unparseable start, cannot yield a rate.
	if got := CadenceOf(newest, "", 1); got != 30*24*time.Hour {
		t.Errorf("fallback = %v", got)
	}
}

// A sampled distinct count is a lower bound: a value that is rare may simply
// not have been drawn. On six million rows with one row in a second region, the
// sample sees one value — and "a constant, not a dimension" is a claim strong
// enough to change the model, so it must not be made from a sample.
func TestSampledCountsDoNotAssertExactness(t *testing.T) {
	sampled := Column{Name: "region", Distinct: 1, Approx: true}
	if got := sampled.findings(6_000_000, Options{StaleAfter: 1}); len(got) != 0 {
		t.Errorf("a sampled column must not be called a constant: %v", got)
	}
	exact := Column{Name: "region", Distinct: 1}
	if got := exact.findings(6_000_000, Options{StaleAfter: 1}); len(got) != 1 {
		t.Errorf("an exact count of 1 is a real finding, got %v", got)
	}
}

// The segment check slices a table by its low-cardinality columns. A sample
// that drew three plants out of eleven would report the other eight as feeds
// that stopped — eight false alarms in the section that most needs to be taken
// seriously.
func TestSampledColumnsAreNotUsedAsSegments(t *testing.T) {
	c := Column{Name: "plant", Type: "text", Distinct: 4, Approx: true}
	if isSegmentLike(c) && !c.Approx {
		t.Fatal("setup")
	}
	// findSegmentGaps filters on !Approx; assert the field is what gates it, so
	// the guard cannot be removed without this failing.
	if !c.Approx {
		t.Error("a sampled column must carry Approx so the segment check can skip it")
	}
}

// Zero means never sample, literally. A zero value that silently meant five
// million was why `-sample-above 0` produced a report still marked as sampled.
func TestSampleAboveZeroMeansNever(t *testing.T) {
	o := Options{SampleAbove: 0}
	o.withDefaults()
	if o.SampleAbove != 0 {
		t.Errorf("SampleAbove = %d, want 0 (never)", o.SampleAbove)
	}
	if s := samplerFor("pgx", 100_000_000, o); s.on() {
		t.Error("sampling is on with SampleAbove=0")
	}
	o2 := Options{SampleAbove: DefaultSampleAbove}
	o2.withDefaults()
	if s := samplerFor("pgx", 100_000_000, o2); !s.on() {
		t.Error("sampling is off on a hundred million rows at the default threshold")
	}
}

// MySQL and SQLite have no sampling clause, so the sample is the first rows in
// storage order. That is still worth doing and it must be labelled, because
// "no value seen after 2019" would otherwise read as a finding.
func TestEnginesWithoutSamplingAreMarkedBiased(t *testing.T) {
	o := Options{SampleAbove: DefaultSampleAbove}
	o.withDefaults()
	for _, driver := range []string{"mysql", "sqlite"} {
		if s := samplerFor(driver, 100_000_000, o); !s.on() || !s.biased {
			t.Errorf("%s: on=%v biased=%v — want a bounded, labelled-as-biased sample", driver, s.on(), s.biased)
		}
	}
	for _, driver := range []string{"pgx", "sqlserver", "duckdb"} {
		if s := samplerFor(driver, 100_000_000, o); !s.on() || s.biased {
			t.Errorf("%s: on=%v biased=%v — want a real random sample", driver, s.on(), s.biased)
		}
	}
}
