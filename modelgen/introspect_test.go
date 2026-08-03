package modelgen

import "testing"

// A quantity is a dense run from zero or one; a code is drawn from an arbitrary
// set. SAP's client column holds 100 and 200, and the draft proposed summing it.
//
// The rule stays narrow on purpose: dropping a real measure is worse than
// leaving a silly one in, because a missing metric is invisible.
func TestCategoricalIntegersAreCodesNotSmallQuantities(t *testing.T) {
	cases := []struct {
		name        string
		distinct    int64
		lo, hi      int64
		categorical bool
	}{
		{"SAP client 100/200", 2, 100, 200, true},
		{"status codes 10/20/30", 3, 10, 30, true},
		{"quantity 1..7", 7, 1, 7, false},
		{"quantity 0..5", 6, 0, 5, false},
		{"a single value", 1, 4, 4, false},
		{"high cardinality", categoricalCeiling + 1, 1, 99999, false},
	}
	for _, c := range cases {
		if got := isCodeNotQuantity(c.distinct, c.lo, c.hi); got != c.categorical {
			t.Errorf("%s: categorical=%v, want %v", c.name, got, c.categorical)
		}
	}
}
