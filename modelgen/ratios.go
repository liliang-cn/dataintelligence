package modelgen

import (
	"fmt"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// Ratios proposes the derived metrics a business actually asks for.
//
// A draft made only of sums is a draft that stops just short of the questions
// people have. Nobody asks for "sum of revenue and sum of cost"; they ask for
// margin. On the first real engagement every ratio worth having — gross margin,
// shrinkage rate, sales per square metre — had to be written by hand, and the
// generator's output read as unfinished because it was.
//
// Two rules keep this from guessing:
//
//   - **Same table only.** Both operands come from one entity, so they share a
//     grain and the ratio is arithmetic rather than a join whose correctness
//     depends on cardinality. A cross-table ratio (loss in one fact table over
//     revenue in another) is a business decision about which pairing means
//     something, and a generator that guesses at those produces plausible
//     metrics nobody asked for — worse than producing none.
//   - **Named, not inferred from data.** There is no value in a column that
//     says "this one is cost". The meaning is in the name and nowhere else, so
//     the match is on names and the result is explicitly a draft.
//
// Everything produced here is non-additive. That is the part that matters: a
// ratio summed across regions, or averaged from monthly averages, is the
// classic wrong number, and declaring it lets the compiler refuse.
func Ratios(t Table, entity string, numericCols []string) []semantic.Metric {
	has := map[string]bool{}
	for _, c := range numericCols {
		has[c] = true
	}

	sum := func(col string) string { return entity + "_" + col + "_sum" }
	out := yields(t, entity, numericCols, sum)

	revenue := firstMatching(numericCols, revenueWords)
	if revenue == "" {
		return out // no value column: whatever the yield pass found, and nothing else
	}

	if cost := firstMatching(numericCols, costWords); cost != "" && cost != revenue {
		out = append(out, semantic.Metric{
			Name:    entity + "_gross_margin",
			Formula: fmt.Sprintf("(%s - %s) / nullif(%s, 0)", sum(revenue), sum(cost), sum(revenue)),
			Description: fmt.Sprintf(
				"Margin: (%s − %s) ÷ %s (auto-generated; confirm this is the margin the business means).",
				revenue, cost, revenue),
			Synonyms:   []string{"margin", "gross margin", strings.ReplaceAll(entity, "_", " ") + " margin"},
			Additivity: semantic.NonAdditive,
		})
	}

	// Value per unit of activity: average order value, average unit price. The
	// denominator is a count in the same row, so this is the same arithmetic a
	// person would do — and the same one they would get wrong by averaging the
	// per-row ratios instead.
	for _, count := range matchingAll(numericCols, countWords) {
		if count == revenue {
			continue
		}
		out = append(out, semantic.Metric{
			Name:    fmt.Sprintf("%s_%s_per_%s", entity, revenue, singular(count)),
			Formula: fmt.Sprintf("%s / nullif(%s, 0)", sum(revenue), sum(count)),
			Description: fmt.Sprintf(
				"%s per %s (auto-generated; an average of totals, not an average of averages).",
				revenue, singular(count)),
			Synonyms:   []string{fmt.Sprintf("%s per %s", revenue, singular(count))},
			Additivity: semantic.NonAdditive,
		})
		if len(out) >= maxRatiosPerEntity {
			break
		}
	}
	return out
}

// maxRatiosPerEntity caps the noise. A wide fact table has half a dozen count
// columns, and six proposed averages bury the one or two that were wanted.
const maxRatiosPerEntity = 4

var (
	revenueWords = []string{"revenue", "gmv", "turnover", "net_sales", "sales_amount", "amount", "sales"}
	costWords    = []string{"cogs", "cost", "expense", "spend"}
	countWords   = []string{"transactions", "orders", "units", "quantity", "qty", "visits", "sessions", "tickets"}
)

// firstMatching returns the column whose name best matches one of the words,
// preferring earlier words: "revenue" beats "amount", because a table with both
// almost certainly means the specific one.
func firstMatching(cols []string, words []string) string {
	for _, w := range words {
		for _, c := range cols {
			if strings.Contains(strings.ToLower(c), w) {
				return c
			}
		}
	}
	return ""
}

func matchingAll(cols []string, words []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range words {
		for _, c := range cols {
			if !seen[c] && strings.Contains(strings.ToLower(c), w) {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// singular trims a trailing "s" for the metric name. Crude, and only ever
// cosmetic — it names a metric, it does not decide one.
func singular(s string) string {
	if len(s) > 3 && strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") {
		return s[:len(s)-1]
	}
	return s
}
