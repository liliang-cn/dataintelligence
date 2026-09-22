package board

import (
	"sort"
	"strings"
	"time"
)

// Rows arrive in whatever order the warehouse returned them, which is none.
//
// On a bar chart that is untidy. On a line chart over time it is a lie: the
// months came back 04, 06, 03, 02, 07, 08, 05, 01 and the renderer drew a
// zigzag joining them in that order. Every point was correct and the line
// meant nothing, which is the most dangerous kind of chart — it looks like a
// trend.
//
// So the host orders the rows before it writes them, by the rule the panel's
// own shape implies:
//
//   - a time grain sorts ascending by the period, because a series read left
//     to right must run forwards
//   - anything else sorts descending by the measure, because the question
//     behind a broken-down panel is which is biggest
//
// Sorting here rather than in SQL is deliberate: the compiler emits the query
// the semantic layer means, and an ORDER BY added for presentation would be a
// change to what was executed for the sake of what is drawn.

func order(p Panel, rows [][]any) [][]any {
	if len(rows) < 2 || len(rows[0]) < 2 {
		return rows
	}
	if p.Grain != "" {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[i][0], rows[j][0]) })
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, aok := asNumber(rows[i][1])
		b, bok := asNumber(rows[j][1])
		if !aok || !bok {
			return false
		}
		return a > b
	})
	return rows
}

// less orders two period labels. They are strings by the time they reach here
// — the formatter below has already run — and the formats it produces all sort
// correctly as text: 2026-01 before 2026-02, 2026-Q1 before 2026-Q2.
func less(a, b any) bool {
	sa, aok := a.(string)
	sb, bok := b.(string)
	if aok && bok {
		return sa < sb
	}
	na, aok := asNumber(a)
	nb, bok := asNumber(b)
	if aok && bok {
		return na < nb
	}
	return false
}

// period renders a time value at the grain that was asked for.
//
// A driver hands back a timestamp and Go's default rendering of one is
// "2026-04-01 08:00:00 +0800 CST" — which on a month-grained axis is both
// unreadable and wrong: it names a day and a zone for a bucket that is a
// month, and the zone is the server's rather than the warehouse's.
func period(t time.Time, grain string) string {
	switch strings.ToLower(strings.TrimSpace(grain)) {
	case "year":
		return t.Format("2006")
	case "quarter":
		return t.Format("2006") + "-Q" + string(rune('0'+(int(t.Month())-1)/3+1))
	case "month":
		return t.Format("2006-01")
	case "week":
		y, w := t.ISOWeek()
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + "-W" + pad2(w)
	case "day", "":
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02")
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
