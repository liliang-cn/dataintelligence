package nleval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/engine"
)

// Mined is one question people actually asked, with what the system made of it.
type Mined struct {
	Question string   `yaml:"question"`
	Metrics  []string `yaml:"expect_metrics"`
	GroupBy  []string `yaml:"expect_dims,omitempty"`
	Refused  bool     `yaml:"expect_refused,omitempty"`
	Times    int      `yaml:"-"` // how often it was asked
	Askers   int      `yaml:"-"` // how many distinct people asked it
	Variants []string `yaml:"-"` // the other phrasings that grounded the same way
}

// MineQuestions reads the audit trail and returns the questions people asked,
// most-asked first.
//
// An eval set written by the engineer is a set of questions the engineer
// already knows the system handles; it measures the imagination of whoever
// wrote it. The audit trail has the real ones, in the words people used, and
// the ones that matter most are the ones asked most often by the most people.
//
// What comes back is a *draft*, and the distinction is load-bearing. The
// expectation on each case is what the system answered, not what is correct.
// Promoting that straight into the gate would freeze today's behaviour as the
// standard — every wrong answer becomes the required answer, and the gate then
// defends the bug. So this returns proposals for a person to check, and the
// file it writes says so on every line.
func MineQuestions(ctx context.Context, eng *engine.Engine, engagement string, limit int) ([]Mined, error) {
	d := eng.Dialect
	where := "WHERE " + d.QuoteIdent("question") + " IS NOT NULL AND " + d.QuoteIdent("question") + " <> ''"
	var args []any
	if engagement != "" {
		where += " AND " + d.QuoteIdent("engagement") + " = " + d.Placeholder(1)
		args = append(args, engagement)
	}
	sql := fmt.Sprintf("SELECT %s, %s, %s, %s, %s FROM _audit %s",
		d.QuoteIdent("question"), d.QuoteIdent("metrics"), d.QuoteIdent("group_by"),
		d.QuoteIdent("refused"), d.QuoteIdent("user"), where)

	res, err := eng.WH.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("no question history to mine: %w", err)
	}
	if len(res.Rows) == 0 && engagement != "" {
		// An engagement filter that matches nothing looks identical to nobody
		// having asked anything. It usually means the deployment was not told
		// which customer it serves, and saying so is the difference between a
		// one-line config fix and an afternoon.
		untagged, _ := eng.WH.Query(ctx, fmt.Sprintf(
			"SELECT count(*) FROM _audit WHERE %s IS NOT NULL AND %s <> ''",
			d.QuoteIdent("question"), d.QuoteIdent("question")))
		if untagged != nil && len(untagged.Rows) > 0 && cell(untagged.Rows[0][0]) != "0" {
			return nil, fmt.Errorf(
				"no questions tagged with engagement %q, but %s question(s) are untagged — "+
					"set `engagement:` in the serving config so the trail records which customer asked",
				engagement, cell(untagged.Rows[0][0]))
		}
	}

	// Group by what the system *did*, not by the words. "上个月营收" and "revenue
	// last month" are one eval case with two phrasings, and listing them
	// separately makes the set look thorough while testing one thing twice.
	type group struct {
		m        Mined
		askers   map[string]bool
		phrasing map[string]int
	}
	groups := map[string]*group{}
	for _, row := range res.Rows {
		if len(row) < 5 {
			continue
		}
		q := strings.TrimSpace(cell(row[0]))
		if q == "" {
			continue
		}
		metrics, dims := parseList(cell(row[1])), parseList(cell(row[2]))
		if len(metrics) == 0 {
			continue // nothing was resolved: not a case, a gap
		}
		key := strings.Join(metrics, ",") + "|" + strings.Join(dims, ",")
		g := groups[key]
		if g == nil {
			g = &group{
				m:        Mined{Metrics: metrics, GroupBy: dims, Refused: truthy(row[3])},
				askers:   map[string]bool{},
				phrasing: map[string]int{},
			}
			groups[key] = g
		}
		g.m.Times++
		g.askers[cell(row[4])] = true
		g.phrasing[q]++
	}

	out := make([]Mined, 0, len(groups))
	for _, g := range groups {
		g.m.Askers = len(g.askers)
		// The canonical phrasing is the one used most; the rest are recorded so
		// the engineer can promote a second case if the wording differs enough
		// to be worth testing on its own.
		type pc struct {
			q string
			n int
		}
		var ps []pc
		for q, n := range g.phrasing {
			ps = append(ps, pc{q, n})
		}
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].n != ps[j].n {
				return ps[i].n > ps[j].n
			}
			return ps[i].q < ps[j].q
		})
		g.m.Question = ps[0].q
		for _, p := range ps[1:] {
			g.m.Variants = append(g.m.Variants, p.q)
		}
		out = append(out, g.m)
	}

	// Most-asked first, then by how many different people — a question ten
	// people ask once is a better test than one somebody scripted.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Askers != out[j].Askers {
			return out[i].Askers > out[j].Askers
		}
		if out[i].Times != out[j].Times {
			return out[i].Times > out[j].Times
		}
		return out[i].Question < out[j].Question
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// RenderMined writes the draft eval set.
//
// It goes to its own file and never into the live set. An eval set is a gate,
// and a gate assembled from the system's own past answers passes by
// construction — it would report high accuracy for agreeing with itself, which
// is the same failure the reconciliation source field exists to prevent.
func RenderMined(customer string, ms []Mined) string {
	var b strings.Builder
	b.WriteString("# Proposed eval cases, mined from questions people actually asked")
	if customer != "" {
		b.WriteString(" — " + customer)
	}
	b.WriteString("\n#\n")
	b.WriteString("# READ BEFORE USE. Each `expect_metrics` below is what the system ANSWERED,\n")
	b.WriteString("# not what is correct. Copying this into the eval set unchecked turns every\n")
	b.WriteString("# wrong answer into the required answer, and the gate then defends the bug.\n")
	b.WriteString("#\n")
	b.WriteString("# Go through them with someone who knows the business, fix the expectations\n")
	b.WriteString("# that are wrong, delete the ones that are not worth testing, and move the\n")
	b.WriteString("# rest into the engagement's evalset.\n\ncases:\n")
	for i, m := range ms {
		fmt.Fprintf(&b, "\n  # asked %d time(s) by %d person(s)", m.Times, m.Askers)
		if len(m.Variants) > 0 {
			fmt.Fprintf(&b, "; also phrased as: %s", strings.Join(quoteAll(m.Variants), ", "))
		}
		fmt.Fprintf(&b, "\n  - name: mined_%02d\n    question: %q\n", i+1, m.Question)
		fmt.Fprintf(&b, "    expect_metrics: [%s]\n", strings.Join(m.Metrics, ", "))
		if len(m.GroupBy) > 0 {
			fmt.Fprintf(&b, "    expect_dims: [%s]\n", strings.Join(m.GroupBy, ", "))
		}
		if m.Refused {
			b.WriteString("    expect_refused: true\n")
		}
	}
	return b.String()
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// parseList reads back the "[a b c]" the audit writer produced.
func parseList(s string) []string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "[]"))
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case string:
		return t == "true" || t == "1" || t == "t"
	default:
		return false
	}
}
