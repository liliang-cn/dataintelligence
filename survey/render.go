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

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
