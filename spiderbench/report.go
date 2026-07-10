package spiderbench

import (
	"fmt"
	"io"
	"sort"
)

// Coverage is the M1 result: how the Spider dev set breaks down by expressibility.
type Coverage struct {
	Total   int
	ByCat   map[Category]int
	Strict  int // Expressible
	Lenient int // Expressible + TopNLabelOnly
}

// Cover classifies every example and tallies coverage.
func Cover(xs []Example) Coverage {
	c := Coverage{Total: len(xs), ByCat: map[Category]int{}}
	for i := range xs {
		cat := Classify(xs[i].SQL)
		c.ByCat[cat]++
		if cat == Expressible {
			c.Strict++
		}
		if cat.InScope() {
			c.Lenient++
		}
	}
	return c
}

// Print writes a human-readable coverage breakdown.
func (c Coverage) Print(w io.Writer) {
	pct := func(n int) float64 {
		if c.Total == 0 {
			return 0
		}
		return float64(n) / float64(c.Total) * 100
	}
	// Stable, most-common-first ordering.
	type row struct {
		cat Category
		n   int
	}
	rows := make([]row, 0, len(c.ByCat))
	for cat, n := range c.ByCat {
		rows = append(rows, row{cat, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].cat < rows[j].cat
	})

	fmt.Fprintf(w, "Spider dev — expressibility coverage (N=%d)\n", c.Total)
	for _, r := range rows {
		mark := "  "
		if r.cat.InScope() {
			mark = "▸ "
		}
		fmt.Fprintf(w, "  %s%-16s %5d  %5.1f%%\n", mark, r.cat, r.n, pct(r.n))
	}
	fmt.Fprintf(w, "\n  coverage (strict, metric in output) : %d/%d = %.1f%%\n", c.Strict, c.Total, pct(c.Strict))
	fmt.Fprintf(w, "  coverage (lenient, + top-N by metric): %d/%d = %.1f%%\n", c.Lenient, c.Total, pct(c.Lenient))
	fmt.Fprintln(w, "\n  ▸ = in scope for the governed semantic layer (metric × dimension).")
	fmt.Fprintln(w, "  The rest are row-level / nested / set-op queries a metric layer does not answer by design.")
}
