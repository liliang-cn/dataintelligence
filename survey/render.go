package survey

import (
	"fmt"
	"io"
	"strings"
)

// WriteMarkdown renders the survey for a human.
//
// Findings come before the inventory. An engineer scrolling this on day two
// wants the three things that will break the model, not a hundred rows of
// column types they can read from the schema.
func (r *Report) WriteMarkdown(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("# Site survey — %s", r.Database)
	p("")
	p("%s · %s", r.Driver, r.TakenAt.Format("2006-01-02 15:04"))
	p("")
	p("Everything below came from a query against the live database. Nothing is")
	p("inferred from table or column names.")

	if len(r.Findings) > 0 {
		p("")
		p("## Summary")
		p("")
		for _, f := range r.Findings {
			p("- %s", f)
		}
	}

	// Per-table findings, gathered up front. These are the arguments to have
	// with the customer before writing a line of the model.
	var flagged []Table
	for _, t := range r.Tables {
		if len(t.Findings) > 0 {
			flagged = append(flagged, t)
		}
	}
	if len(flagged) > 0 {
		p("")
		p("## Things to ask about (%d table(s))", len(flagged))
		p("")
		for _, t := range flagged {
			p("**%s** — %s rows", t.Name, humanInt(t.Rows))
			p("")
			for _, f := range t.Findings {
				p("- %s", f)
			}
			p("")
		}
	}

	if len(r.Gaps) > 0 {
		p("## Parts of a table that stopped while the rest kept going")
		p("")
		p("A table-level check cannot see these: the table still moves because the")
		p("other segments report. The totals are simply missing one.")
		p("")
		p("| Table | Segment | Last reported | Periods behind |")
		p("|---|---|---|---:|")
		for _, g := range r.Gaps {
			p("| `%s` | `%s` = %s | %s | %d |", g.Table, g.By, g.Segment, g.Newest, g.Behind)
		}
		p("")
	}

	if len(r.Implied) > 0 {
		p("## References with no foreign key behind them")
		p("")
		p("These columns name another table's key and mostly match it, but nothing")
		p("enforces that. Cross-system code matching never has a constraint — which")
		p("is why it drifts, and why the declared-key check above cannot see it.")
		p("")
		p("| From | Looks like | Values that do not match |")
		p("|---|---|---:|")
		for _, i := range r.Implied {
			p("| `%s.%s` | `%s.%s` | %s of %s |", i.Table, i.Column, i.RefTable, i.RefColumn,
				humanInt(i.Orphans), humanInt(i.Total))
		}
		p("")
	}

	if len(r.Orphans) > 0 {
		p("## Foreign keys the data does not honour")
		p("")
		p("A join on any of these silently drops rows. The join graph has to")
		p("account for them, or the model has to leave them out.")
		p("")
		p("| From | To | Orphan rows |")
		p("|---|---|---:|")
		for _, o := range r.Orphans {
			p("| `%s.%s` | `%s.%s` | %s |", o.Table, o.Column, o.RefTable, o.RefColumn, humanInt(o.Count))
		}
		p("")
	}

	p("## Inventory")
	p("")
	p("| Table | Rows | Key | Columns |")
	p("|---|---:|---|---:|")
	for _, t := range r.Tables {
		p("| `%s` | %s | %s | %d |", t.Name, humanInt(t.Rows), orDash(t.PrimaryKey), len(t.Columns))
	}

	p("")
	p("## Columns")
	for _, t := range r.Tables {
		p("")
		p("### `%s`", t.Name)
		p("")
		p("| Column | Type | Null | Distinct | Range / values |")
		p("|---|---|---:|---:|---|")
		for _, c := range t.Columns {
			p("| `%s` | %s | %s | %s | %s |",
				c.Name, c.Type, pct(c.Nulls), distinct(c.Distinct), rangeOrValues(c))
		}
	}
}

func pct(f float64) string {
	if f == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// distinct says "many" rather than a number when the probe was capped. A made-up
// exact figure would be worse than an honest bound.
func distinct(d int64) string {
	if d < 0 {
		return "many"
	}
	return fmt.Sprintf("%d", d)
}

func rangeOrValues(c Column) string {
	if c.Min != "" || c.Max != "" {
		return fmt.Sprintf("%s → %s", orDash(c.Min), orDash(c.Max))
	}
	if len(c.Sample) == 0 {
		return ""
	}
	vals := make([]string, 0, len(c.Sample))
	for _, s := range c.Sample {
		vals = append(vals, "`"+truncate(s, 24)+"`")
	}
	return strings.Join(vals, " ")
}

// truncate cuts by runes, not bytes.
//
// Slicing a Go string by byte splits a multi-byte character in half and puts
// invalid UTF-8 into the document — which is how a survey of a warehouse whose
// data is Chinese produces a file the customer's own tools refuse to open. A
// sample value is exactly where non-ASCII shows up first.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
