package modelgen

import (
	"fmt"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// yields proposes the ratios a plant runs on: how much came out for what went
// in, and how much of it was scrap.
//
// The margin and per-unit rules next door were written against a retail
// warehouse and only know how to divide money. A foundry's schema has no
// revenue column at all — it has charged tonnes and tapped tonnes, poured
// pieces and good pieces — and the generator produced a draft of thirty-eight
// sums with not one ratio in it, on a customer whose entire business is
// measured in 一次合格率 and 收得率. Same table, same grain, same arithmetic as
// margin; only the vocabulary was missing.
//
// Pairing is on the **unit**, not on position: two columns are comparable when
// their names end in the same unit token (`_qty`, `_t`, `_kg`), because that is
// what makes the ratio dimensionless and therefore meaningful. Dividing pieces
// by tonnes produces a number, and a number with no unit is how a wrong metric
// survives review.
func yields(t Table, entity string, numericCols []string, sum func(string) string) []semantic.Metric {
	byUnit := map[string][]roleCol{}
	for _, c := range numericCols {
		if rc, ok := classify(c); ok {
			byUnit[rc.unit] = append(byUnit[rc.unit], rc)
		}
	}

	var out []semantic.Metric
	for _, unit := range sortedKeys(byUnit) {
		cols := byUnit[unit]
		var input string
		for _, c := range cols {
			if c.role == roleInput {
				input = c.name
				break
			}
		}
		if input == "" {
			continue // a yield needs something to have gone in
		}
		for _, c := range cols {
			if c.name == input {
				continue
			}
			var name, desc string
			var syn []string
			switch c.role {
			case roleOutput:
				name = entity + "_yield_rate"
				desc = fmt.Sprintf(
					"Yield: %s ÷ %s (auto-generated from column names; confirm the denominator — "+
						"whether rework counts as good is a business decision, not a schema one).",
					c.name, input)
				syn = []string{"yield", "合格率", "收得率", strings.ReplaceAll(entity, "_", " ") + " yield"}
			case roleScrap:
				name = entity + "_" + c.name + "_rate"
				desc = fmt.Sprintf("%s as a share of %s (auto-generated from column names).", c.name, input)
				syn = []string{c.name + " rate", "废品率"}
			default:
				continue
			}
			out = append(out, semantic.Metric{
				Name:        name,
				Formula:     fmt.Sprintf("%s / nullif(%s, 0)", sum(c.name), sum(input)),
				Description: desc,
				Synonyms:    syn,
				Additivity:  semantic.NonAdditive,
			})
			if len(out) >= maxRatiosPerEntity {
				return out
			}
		}
	}
	return out
}

type role int

const (
	roleInput role = iota
	roleOutput
	roleScrap
)

type roleCol struct {
	name string
	unit string
	role role
}

// classify reads a column name as <role>_<unit>, matching on whole tokens.
//
// Substring matching cannot be used here: "in" appears inside "inspection" and
// "line", and a rule that reads `line_id` as an input quantity proposes ratios
// between things that were never meant to divide.
func classify(col string) (roleCol, bool) {
	parts := strings.Split(strings.ToLower(col), "_")
	if len(parts) < 2 {
		return roleCol{}, false
	}
	unit := parts[len(parts)-1]
	if !unitTokens[unit] {
		return roleCol{}, false
	}
	for _, p := range parts[:len(parts)-1] {
		switch {
		case inputTokens[p]:
			return roleCol{col, unit, roleInput}, true
		case outputTokens[p]:
			return roleCol{col, unit, roleOutput}, true
		case scrapTokens[p]:
			return roleCol{col, unit, roleScrap}, true
		}
	}
	return roleCol{}, false
}

// A unit the ratio can be taken in. Money is deliberately absent — margin is
// next door and means something different.
var unitTokens = set("qty", "quantity", "t", "tons", "tonnes", "kg", "pcs", "pieces", "units", "count", "num")

var (
	inputTokens  = set("in", "input", "charged", "charge", "issued", "fed", "feed", "poured", "started", "checked", "inspected", "sampled", "planned")
	outputTokens = set("out", "output", "good", "ok", "passed", "pass", "accepted", "tapped", "finished", "completed", "yield")
	scrapTokens  = set("scrap", "scrapped", "reject", "rejected", "defect", "defective", "rework", "waste", "loss")
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// sortedKeys keeps the generated draft byte-identical between runs. A model file
// that reorders itself on every regeneration produces a diff nobody reads.
func sortedKeys(m map[string][]roleCol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
