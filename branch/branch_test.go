package branch

import (
	"strings"
	"testing"
)

func TestSafeNameKeepsTheNameOutOfTheSQL(t *testing.T) {
	for _, bad := range []string{"", "a b", "a-b", `a"b`, "a;DROP", strings.Repeat("x", 41)} {
		if _, err := safeName(bad); err == nil {
			t.Errorf("safeName(%q) should be rejected", bad)
		}
	}
	for _, ok := range []string{"jan", "load_2026_01", "A1"} {
		if _, err := safeName(ok); err != nil {
			t.Errorf("safeName(%q) = %v", ok, err)
		}
	}
}

func TestPctHandlesAZeroBaseline(t *testing.T) {
	// Growing from nothing has no ratio; reporting 100 beats +Inf or a silent 0.
	if got := Pct(0, 0); got != 0 {
		t.Errorf("Pct(0,0) = %v", got)
	}
	if got := Pct(0, 5); got != 100 {
		t.Errorf("Pct(0,5) = %v, want 100", got)
	}
	if got := Pct(200, 300); got != 50 {
		t.Errorf("Pct(200,300) = %v, want 50", got)
	}
	if got := Pct(300, 200); got != -33.33 {
		t.Errorf("Pct(300,200) = %v, want -33.33", got)
	}
}

// The three ways a batch is wrong while every row in it is valid.
func TestFlagsCatchTheAggregateLevelFailures(t *testing.T) {
	has := func(flags []string, sub string) bool {
		for _, f := range flags {
			if strings.Contains(f, sub) {
				return true
			}
		}
		return false
	}

	// The same file loaded twice: rows exactly double, totals double with them.
	dup := Flags(1000, 2000, map[string]SumDiff{"amount": {Main: 500, Branch: 1000, Pct: 100}})
	if !has(dup, "doubled") {
		t.Errorf("duplicate load not flagged: %v", dup)
	}

	// Yuan became cents: the row count does not move, the total explodes.
	unit := Flags(1000, 1000, map[string]SumDiff{"amount": {Main: 500, Branch: 50000, Pct: 9900}})
	if !has(unit, "unit or definition change") {
		t.Errorf("unit change not flagged: %v", unit)
	}

	// A store stopped reporting: fewer rows than production.
	missing := Flags(1000, 820, map[string]SumDiff{})
	if !has(missing, "fewer rows") {
		t.Errorf("missing rows not flagged: %v", missing)
	}

	// A normal incremental load: more rows, totals up in proportion. No noise.
	normal := Flags(1000, 1050, map[string]SumDiff{"amount": {Main: 500, Branch: 525, Pct: 5}})
	if len(normal) != 0 {
		t.Errorf("a normal load should raise nothing, got %v", normal)
	}
}

// The first live run of this flagged an ordinary +60-row load, because summing
// a climbing primary key produced a 66744% "change". A gate that fires on every
// normal load is one people learn to click past.
func TestKeyColumnsAreNotMeasures(t *testing.T) {
	// order_id is excluded upstream, so what reaches Flags is measures only.
	normal := Flags(3000, 3060, map[string]SumDiff{
		"amount": {Main: 1377020, Branch: 1395020, Pct: 1.31},
		"qty":    {Main: 9000, Branch: 9120, Pct: 1.33},
	})
	if len(normal) != 0 {
		t.Errorf("an ordinary incremental load should raise nothing, got %v", normal)
	}
}
