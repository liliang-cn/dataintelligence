package modelgen

import (
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

func names(ms []semantic.Metric) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}

func find(ms []semantic.Metric, name string) *semantic.Metric {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}

// Nobody asks for "sum of revenue and sum of cost"; they ask for margin. On the
// first real engagement every ratio worth having had to be written by hand.
func TestMarginIsProposedWhenBothSidesArePresent(t *testing.T) {
	got := Ratios(Table{Name: "sales"}, "sale",
		[]string{"revenue", "cogs_amount", "transactions", "units"})

	m := find(got, "sale_gross_margin")
	if m == nil {
		t.Fatalf("no margin proposed: %v", names(got))
	}
	if !strings.Contains(m.Formula, "sale_revenue_sum") || !strings.Contains(m.Formula, "sale_cogs_amount_sum") {
		t.Errorf("formula should reference the generated sums: %q", m.Formula)
	}
	// Dividing by zero revenue must not produce an error or an infinity.
	if !strings.Contains(m.Formula, "nullif") {
		t.Errorf("formula should guard the denominator: %q", m.Formula)
	}
	// The part that matters most: a ratio summed across regions, or averaged
	// from monthly averages, is the classic wrong number. Declaring it lets the
	// compiler refuse.
	for _, r := range got {
		if r.Additivity != semantic.NonAdditive {
			t.Errorf("%s must be non-additive, got %q", r.Name, r.Additivity)
		}
	}
}

func TestPerUnitRatiosUseTheCountsInTheSameTable(t *testing.T) {
	got := names(Ratios(Table{Name: "sales"}, "sale",
		[]string{"revenue", "transactions", "units"}))
	for _, want := range []string{"sale_revenue_per_transaction", "sale_revenue_per_unit"} {
		if !has(got, want) {
			t.Errorf("missing %s: %v", want, got)
		}
	}
}

// Both operands come from one table, so they share a grain and the ratio is
// arithmetic rather than a join whose correctness depends on cardinality.
// Which cross-table pairing means something is a business decision, and a
// generator that guesses produces plausible metrics nobody asked for.
func TestNothingIsProposedWithoutAValueColumn(t *testing.T) {
	if got := Ratios(Table{Name: "shrinkage"}, "shrinkage", []string{"loss_qty"}); len(got) != 0 {
		t.Errorf("no value column means no ratio worth proposing, got %v", names(got))
	}
	if got := Ratios(Table{Name: "stores"}, "store", []string{"area_sqm"}); len(got) != 0 {
		t.Errorf("a dimension attribute is not a numerator: %v", names(got))
	}
}

// A wide fact table has half a dozen count columns; six proposed averages bury
// the one or two that were wanted.
func TestProposalsAreCapped(t *testing.T) {
	got := Ratios(Table{Name: "wide"}, "wide", []string{
		"revenue", "cost", "transactions", "orders", "units", "quantity", "visits", "sessions", "tickets",
	})
	if len(got) > maxRatiosPerEntity+1 {
		t.Errorf("too many proposals (%d): %v", len(got), names(got))
	}
}

// A table with both `revenue` and `amount` almost certainly means the specific
// one; picking `amount` would compute a margin against the wrong numerator.
func TestTheMoreSpecificValueColumnWins(t *testing.T) {
	got := Ratios(Table{Name: "t"}, "t", []string{"amount", "revenue", "cost"})
	m := find(got, "t_gross_margin")
	if m == nil || !strings.Contains(m.Formula, "t_revenue_sum") {
		t.Errorf("margin should be built on revenue, not amount: %v", m)
	}
}

func TestSingularOnlyRenamesMetrics(t *testing.T) {
	for in, want := range map[string]string{
		"transactions": "transaction", "units": "unit", "qty": "qty", "sessions": "session",
		"gross": "gross", // -ss is not a plural
	} {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
}

// The generator must find the ratios a plant runs on, not only the ones a shop
// runs on. This schema has no revenue column anywhere.
func TestYieldRatiosOnAManufacturingTable(t *testing.T) {
	cases := []struct {
		entity string
		cols   []string
		want   []string // metric name → formula fragment
	}{
		{"melting", []string{"charge_t", "tapped_t", "scrap_t"},
			[]string{"melting_yield_rate", "melting_scrap_t_rate"}},
		{"cleaning", []string{"in_qty", "out_qty", "rework_qty"},
			[]string{"cleaning_yield_rate", "cleaning_rework_qty_rate"}},
		{"inspection", []string{"checked_qty", "defect_qty"},
			[]string{"inspection_defect_qty_rate"}},
	}
	for _, c := range cases {
		got := Ratios(Table{Name: c.entity}, c.entity, c.cols)
		names := map[string]bool{}
		for _, m := range got {
			names[m.Name] = true
			if m.Additivity != semantic.NonAdditive {
				t.Errorf("%s: %s must be non-additive — a yield summed across plants is the classic wrong number", c.entity, m.Name)
			}
		}
		for _, w := range c.want {
			if !names[w] {
				t.Errorf("%s: missing %s, got %v", c.entity, w, keysOf(names))
			}
		}
	}
}

// The ratio must be dimensionless: pieces over tonnes is a number with no unit,
// and a number with no unit is how a wrong metric survives review.
func TestYieldsDoNotDivideAcrossUnits(t *testing.T) {
	for _, m := range Ratios(Table{Name: "pouring"}, "pouring", []string{"charge_t", "good_qty"}) {
		t.Errorf("proposed %s = %s across units", m.Name, m.Formula)
	}
}

// "in" is a substring of "inspection" and of "line". Matching on substrings
// proposes ratios between things that were never meant to divide.
func TestYieldsMatchWholeTokensNotSubstrings(t *testing.T) {
	for _, m := range Ratios(Table{Name: "batch"}, "batch", []string{"line_id", "inspection_id", "shift_id"}) {
		t.Errorf("proposed %s = %s from an id column", m.Name, m.Formula)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
