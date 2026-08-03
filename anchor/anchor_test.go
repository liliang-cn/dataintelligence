package anchor

import "testing"

// A customer who writes 97.3 has told you the precision they have. Any default
// this code picked instead would be wrong in one direction or the other.
func TestToleranceComesFromTheFiguresOwnPrecision(t *testing.T) {
	cases := map[string]float64{
		"97.3":     0.05,
		"97.30":    0.005,
		"0.973":    0.0005,
		"94":       0.5,
		" 1842.30": 0.005,
		"1.5e3":    0.05,
	}
	for written, want := range cases {
		if got := ToleranceOf(written); got != want {
			t.Errorf("ToleranceOf(%q) = %g, want %g", written, got, want)
		}
	}
}

// Everything matching is the failure that looks most like success: the search
// did not find the customer's scope, it found that the figure cannot identify
// one.
func TestTooCoarseIsDistinctFromAmbiguous(t *testing.T) {
	coarse := &Result{Tol: 0.0005, Lo: 0.962, Hi: 0.9625,
		Matches: []Candidate{{Value: 0.962}, {Value: 0.9625}}}
	if !coarse.TooCoarse() {
		t.Error("a metric that barely varies cannot be anchored by a rounded figure")
	}
	// Genuinely ambiguous: the metric moves, and two distinct scopes happen to
	// land on the same number. Asking for more decimals will not help.
	ambiguous := &Result{Tol: 0.0005, Lo: 0.10, Hi: 0.99,
		Matches: []Candidate{{Value: 0.973}, {Value: 0.973}}}
	if ambiguous.TooCoarse() {
		t.Error("a metric with real spread is ambiguous, not coarse — different advice")
	}
	// One match is an anchor, whatever the spread.
	if (&Result{Tol: 1, Lo: 0, Hi: 1, Matches: []Candidate{{Value: 1}}}).TooCoarse() {
		t.Error("a single match is not ambiguous")
	}
}

// Half-open bounds, so the recon file says what "= Q2" only implies.
func TestWindowBounds(t *testing.T) {
	for _, c := range []struct{ in, grain, from, to string }{
		{"2026-04-01", "quarter", "2026-04-01", "2026-07-01"},
		{"2026-04-01", "month", "2026-04-01", "2026-05-01"},
		{"2026-01-01", "year", "2026-01-01", "2027-01-01"},
	} {
		from, to, ok := window(c.in, c.grain)
		if !ok || from != c.from || to != c.to {
			t.Errorf("window(%s, %s) = %s..%s, want %s..%s", c.in, c.grain, from, to, c.from, c.to)
		}
	}
}
