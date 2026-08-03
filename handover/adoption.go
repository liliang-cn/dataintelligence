package handover

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/engine"
	semantic "github.com/liliang-cn/semantic-go"
)

// Adoption is who used this and what they never touched.
//
// A delivery report proves the numbers are right. It says nothing about whether
// anyone opens the thing, and a correct dashboard nobody looks at is worth
// nothing. The audit trail already holds the answer; until now nothing read it
// back.
//
// The interesting half is the negative. Metrics that were modelled, argued
// about, and then never asked for are the clearest signal available about where
// the modelling effort was misspent — and the cheapest thing to hand back to
// whoever commissioned it.
type Adoption struct {
	Database   string        `json:"database"`
	Engagement string        `json:"engagement,omitempty"`
	Days       int           `json:"days"`
	Queries    int           `json:"queries"`
	Refused    int           `json:"refused"`
	Users      []UserUsage   `json:"users,omitempty"`
	Metrics    []MetricUsage `json:"metrics,omitempty"`
	Unused     []string      `json:"unused_metrics,omitempty"`
}

// UserUsage is one caller's activity.
type UserUsage struct {
	User    string `json:"user"`
	Role    string `json:"role"`
	Queries int    `json:"queries"`
}

// MetricUsage is how often a metric was actually asked for.
type MetricUsage struct {
	Metric  string `json:"metric"`
	Queries int    `json:"queries"`
}

// Measure reads the audit trail.
//
// It reads what was recorded, which is not the same as what happened: the trail
// starts when auditing does, and a period with no rows is indistinguishable
// from a period nobody worked. Days is reported alongside every figure so a
// small number is read as a short window rather than as apathy.
// Measure reads the trail for one engagement.
//
// The engagement filter is not a convenience. One deployment serving several
// customers writes every trail into one table, and an adoption report that
// silently mixes them tells a customer that people are using metrics they do
// not have — and, worse, is the moment somebody hands over another customer's
// usage along with their own.
func Measure(ctx context.Context, eng *engine.Engine, database, engagement string, days int) (*Adoption, error) {
	if days <= 0 {
		days = 30
	}
	a := &Adoption{Database: database, Days: days, Engagement: engagement}

	since := sinceExpr(eng.WH.Driver(), days)
	where := "ts >= " + since
	var args []any
	if engagement != "" {
		where += " AND " + eng.Dialect.QuoteIdent("engagement") + " = " + eng.Dialect.Placeholder(1)
		args = append(args, engagement)
	}
	rows, err := eng.WH.Query(ctx, fmt.Sprintf(
		`SELECT %s, role, metrics, refused FROM _audit WHERE %s`,
		eng.Dialect.QuoteIdent("user"), where), args...)
	if err != nil {
		// No trail is a finding, not a failure: it means nobody has asked
		// anything, or auditing never wrote — and the caller should be told
		// which rather than shown an empty report.
		return nil, fmt.Errorf("no audit trail to read (%w) — has anyone asked a question yet?", err)
	}

	byUser := map[string]*UserUsage{}
	byMetric := map[string]int{}
	for _, row := range rows.Rows {
		user, role := str(row[0]), str(row[1])
		if refused(row[3]) {
			a.Refused++
			continue
		}
		a.Queries++

		key := user + "\x00" + role
		u, ok := byUser[key]
		if !ok {
			u = &UserUsage{User: user, Role: role}
			byUser[key] = u
		}
		u.Queries++

		for _, m := range splitMetrics(str(row[2])) {
			byMetric[m]++
		}
	}

	for _, u := range byUser {
		a.Users = append(a.Users, *u)
	}
	sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].Queries > a.Users[j].Queries })

	for m, n := range byMetric {
		a.Metrics = append(a.Metrics, MetricUsage{Metric: m, Queries: n})
	}
	sort.Slice(a.Metrics, func(i, j int) bool { return a.Metrics[i].Queries > a.Metrics[j].Queries })

	if eng.Model != nil {
		a.Unused = unusedMetrics(eng.Model, byMetric)
	}
	return a, nil
}

func unusedMetrics(m *semantic.Model, used map[string]int) []string {
	var out []string
	for i := range m.Metrics {
		if used[m.Metrics[i].Name] == 0 {
			out = append(out, m.Metrics[i].Name)
		}
	}
	sort.Strings(out)
	return out
}

// sinceExpr is the "last N days" predicate, per engine. now() - interval is
// Postgres-only, and getting this wrong returns everything rather than erroring.
func sinceExpr(driver string, days int) string {
	switch driver {
	case "mysql":
		return fmt.Sprintf("DATE_SUB(NOW(), INTERVAL %d DAY)", days)
	case "sqlite":
		return fmt.Sprintf("datetime('now', '-%d days')", days)
	case "sqlserver":
		return fmt.Sprintf("DATEADD(day, -%d, SYSUTCDATETIME())", days)
	default:
		return fmt.Sprintf("now() - interval '%d days'", days)
	}
}

// WriteMarkdown renders adoption for whoever paid for the delivery.
func (a *Adoption) WriteMarkdown(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("# Adoption — %s", orDash(a.Database))
	p("")
	p("Last %d days, read from the audit trail.", a.Days)
	p("")

	if a.Queries == 0 {
		p("**Nobody asked anything.** Either it has not been rolled out, or it has and")
		p("is not being used. Those are different problems and only you can tell them")
		p("apart — but a correct answer nobody asks for is worth the same as a wrong one.")
		return
	}

	p("**%d question(s)** from **%d person(s)**.", a.Queries, len(a.Users))
	if a.Refused > 0 {
		// Refusals are governance working. Worth showing, since a high count
		// usually means a role is wrong rather than that people are prying.
		p("")
		p("%d request(s) were refused by governance. A steady stream of those usually")
		p("means somebody is in the wrong role, not that they are prying.", a.Refused)
	}

	p("")
	p("## Who")
	p("")
	p("| User | Role | Questions |")
	p("|---|---|---:|")
	for _, u := range a.Users {
		p("| %s | %s | %d |", orDash(u.User), orDash(u.Role), u.Queries)
	}

	if len(a.Metrics) > 0 {
		p("")
		p("## What they asked for")
		p("")
		p("| Metric | Questions |")
		p("|---|---:|")
		for _, m := range a.Metrics {
			p("| `%s` | %d |", m.Metric, m.Queries)
		}
	}

	if len(a.Unused) > 0 {
		p("")
		p("## Modelled and never asked for (%d)", len(a.Unused))
		p("")
		p("Each of these was defined, argued about, and checked. In %d days nobody has", a.Days)
		p("asked for one. That is either work that should not have been done, or a")
		p("metric nobody knows exists — worth finding out which before modelling more.")
		p("")
		for _, m := range a.Unused {
			p("- `%s`", m)
		}
	}
}

// Summary is the line for a terminal.
func (a *Adoption) Summary() string {
	if a.Queries == 0 {
		return fmt.Sprintf("no questions asked in %d days — rolled out, or not?", a.Days)
	}
	s := fmt.Sprintf("%d question(s) from %d person(s) in %d days", a.Queries, len(a.Users), a.Days)
	if n := len(a.Unused); n > 0 {
		s += fmt.Sprintf(" · %d metric(s) never asked for", n)
	}
	return s
}

// splitMetrics parses the trail's "[a b]" rendering of a metric list.
func splitMetrics(s string) []string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "[]"))
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

func refused(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	case []byte:
		return string(t) == "true" || string(t) == "1"
	default:
		return false
	}
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
