package reported

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Headline is the comparison in one sentence, and the sentence is chosen by
// cause before anything else.
//
// This is where "a number that moved because a definition was re-signed and
// a number that moved because late data arrived must never look the same"
// is kept. The first word differs by cause, the approver of each definition
// is named in the same line, and a movement that could not be attributed says
// so instead of borrowing the nearest label.
func (c *Comparison) Headline() string {
	r := c.Report
	when := r.At.Format("2006-01-02")
	switch {
	case c.Now.Err != "" && !c.Frame.DefinitionChanged && !c.Frame.AccessChanged:
		return fmt.Sprintf("NOT REPRODUCIBLE — %s was reported on %s under definition %s, which is still the definition, and the same query now fails: %s",
			r.Name, when, r.ModelHash, c.Now.Err)
	case len(c.Causes) == 0 && (c.Frame.DefinitionChanged || c.Frame.AccessChanged):
		return fmt.Sprintf("UNCHANGED FIGURES — every figure in %s (%s) is what was reported, although %s since",
			r.Name, when, c.frameSentence())
	case len(c.Causes) == 0:
		return fmt.Sprintf("UNCHANGED — %s (%s): same definition %s, same access policy, same figures",
			r.Name, when, r.ModelHash)
	}

	var parts []string
	for _, cause := range c.Causes {
		switch cause {
		case CauseDefinition:
			parts = append(parts, fmt.Sprintf("DEFINITION CHANGED — reported under %s (%s), computed today under %s (%s)",
				r.ModelHash, approvalText(r.Approval), c.Now.ModelHash, approvalText(c.Now.Approval)))
		case CauseAccess:
			parts = append(parts, fmt.Sprintf("ACCESS POLICY CHANGED — reported under policy %s, computed today under %s",
				r.PolicyHash, c.Now.PolicyHash))
		case CauseData:
			parts = append(parts, fmt.Sprintf("DATA CHANGED — under the reported definition %s (%s), the warehouse now holds different figures",
				r.ModelHash, approvalText(r.Approval)))
		}
	}
	line := strings.Join(parts, "; ")
	if c.Split != nil && !c.Split.Separated {
		line += "; whether the data also moved cannot be told: " + c.Split.Why
	}
	if c.Now.Err != "" {
		line += "; today's definition cannot compute this query: " + c.Now.Err
	}
	return line
}

func (c *Comparison) frameSentence() string {
	var s []string
	if c.Frame.DefinitionChanged {
		s = append(s, fmt.Sprintf("the definition changed from %s to %s (%s)",
			c.Report.ModelHash, c.Now.ModelHash, approvalText(c.Now.Approval)))
	}
	if c.Frame.AccessChanged {
		s = append(s, "the access policy changed")
	}
	return strings.Join(s, " and ")
}

func approvalText(a Approval) string {
	switch {
	case !a.Checked:
		return "approval not looked up"
	case a.Err != "":
		return "approval could not be read: " + a.Err
	case a.SignedBy != "" && a.Promoted:
		return "approved by " + a.SignedBy + " on " + a.SignedAt
	case a.SignedBy != "":
		return "signed by " + a.SignedBy + " but never promoted"
	default:
		return "NOBODY approved it"
	}
}

// Text renders the comparison for a terminal: the headline, then what was
// reported and by whom, then only the rows that are not the same — and, when
// the movement was separated, which rows each cause moved.
func (c *Comparison) Text() string {
	var b strings.Builder
	r := c.Report
	fmt.Fprintf(&b, "%s\n\n", c.Headline())
	fmt.Fprintf(&b, "reported %s by %s on %s as role %q", r.Name, r.By, r.At.Format(time.RFC3339), r.Who.Role)
	if r.Question != "" {
		fmt.Fprintf(&b, " — %q", r.Question)
	}
	b.WriteString("\n")
	if r.SupersededBy != "" {
		fmt.Fprintf(&b, "this report was later corrected by %s\n", r.SupersededBy)
	}
	if r.Supersedes != "" {
		fmt.Fprintf(&b, "this report corrects %s: %s\n", r.Supersedes, r.Note)
	}

	section := func(title string, rows []RowDiff) {
		rows = movedOnly(rows)
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s\n", title)
		for _, d := range rows {
			key := strings.Join(d.Key, " | ")
			if key == "" {
				key = "(total)"
			}
			for _, cell := range d.Cells {
				if d.Status == Moved && !cell.Moved {
					continue
				}
				fmt.Fprintf(&b, "  %-9s %s · %s: %s → %s", d.Status, key, cell.Column, side(cell.Then, cell.Absent == "then"), side(cell.Now, cell.Absent == "now"))
				if cell.Delta != nil {
					sign := ""
					if *cell.Delta > 0 {
						sign = "+"
					}
					fmt.Fprintf(&b, " (%s%s)", sign, display(*cell.Delta))
				}
				b.WriteString("\n")
			}
		}
	}
	if c.Split != nil && c.Split.Separated {
		section("moved by the data (reported definition, then vs today):", c.Split.Data)
		label := "moved by the definition (today's data, reported definition vs today's):"
		if !c.Frame.DefinitionChanged {
			label = "moved by the access policy (today's data, reported policy vs today's):"
		}
		section(label, c.Split.Frame)
	} else {
		section("rows that differ:", c.Rows)
	}
	return b.String()
}

func side(v any, absent bool) string {
	if absent {
		return "—"
	}
	return display(v)
}

// display is brief's rule for a person reading a figure: six decimals is
// enough, and a label is left exactly as it came. The record itself keeps
// every digit; only this rendering rounds.
func display(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case float64:
		return trim(strconv.FormatFloat(t, 'f', 6, 64))
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case json.Number, string, []byte:
		s := fmt.Sprintf("%s", t)
		if i := strings.Index(s, "."); i >= 0 && len(s)-i-1 > 6 {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return trim(strconv.FormatFloat(f, 'f', 6, 64))
			}
		}
		return s
	}
	return fmt.Sprint(v)
}

func trim(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}
