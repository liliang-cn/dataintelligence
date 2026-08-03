package survey

import (
	"strings"
	"testing"
	"time"
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
