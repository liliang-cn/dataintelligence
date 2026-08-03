// Package handover covers Day 2: the day after the engineer leaves.
//
// The literature names this the top failure mode — the client team cannot
// maintain what was delivered — and it is not usually a skills problem. It is
// that nothing tells them when the thing has quietly stopped being true. A
// column gets renamed, a feed stops, a definition is changed by someone who did
// not know it was load-bearing, and the answers keep coming out clean and wrong.
//
// So the handover is two artefacts: a check that fails loudly, and a document
// naming who owns what.
package handover

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/nleval"
)

// Drift is what changed underneath the model since it was delivered.
type Drift struct {
	Database string `json:"database"`

	MissingTables  []string     `json:"missing_tables,omitempty"`
	MissingColumns []ColumnRef  `json:"missing_columns,omitempty"`
	StaleFeeds     []StaleFeed  `json:"stale_feeds,omitempty"`
	Failing        []FailedCase `json:"failing_metrics,omitempty"`

	CheckedAt time.Time `json:"checked_at"`
}

// ColumnRef is a model reference to something the warehouse no longer has.
type ColumnRef struct {
	Entity string `json:"entity"`
	Table  string `json:"table"`
	Column string `json:"column"`
	UsedBy string `json:"used_by"` // the dimension or metric that depends on it
}

// StaleFeed is a time dimension whose newest row stopped moving.
type StaleFeed struct {
	Dimension string `json:"dimension"`
	Table     string `json:"table"`
	Column    string `json:"column"`
	Newest    string `json:"newest"`
	Days      int    `json:"days_behind"`
}

// FailedCase is a metric that no longer matches its control query.
type FailedCase struct {
	Metric string  `json:"metric"`
	Got    float64 `json:"got"`
	Want   float64 `json:"want"`
	Source string  `json:"source,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// Clean reports whether nothing needs attention.
func (d *Drift) Clean() bool {
	return len(d.MissingTables) == 0 && len(d.MissingColumns) == 0 &&
		len(d.StaleFeeds) == 0 && len(d.Failing) == 0
}

// Check compares the delivered model against the warehouse as it is now.
//
// The three things checked are the three ways a delivery dies quietly:
// something it depends on was renamed, a source stopped sending, or a metric
// stopped agreeing with the figure it was signed off against. None of them
// raises an error on its own — every query still runs, and the answers still
// look like answers.
func Check(ctx context.Context, eng *engine.Engine, set *nleval.ReconSet, staleAfter time.Duration) (*Drift, error) {
	if eng.Model == nil {
		return nil, fmt.Errorf("drift needs a semantic model — an unmodelled database has nothing to drift from")
	}
	d := &Drift{CheckedAt: time.Now()}

	schema, err := modelgen.Introspect(ctx, eng.WH)
	if err != nil {
		return nil, err
	}
	live := map[string]map[string]bool{} // table → column → exists
	for _, t := range schema.Tables {
		cols := map[string]bool{}
		for _, c := range t.Columns {
			cols[strings.ToLower(c.Name)] = true
		}
		live[strings.ToLower(t.Name)] = cols
	}

	tableOf := map[string]string{}
	for i := range eng.Model.Entities {
		e := &eng.Model.Entities[i]
		tableOf[e.Name] = e.Table
		if _, ok := live[strings.ToLower(e.Table)]; !ok {
			d.MissingTables = append(d.MissingTables, e.Table)
		}
	}

	// Dimensions name their column explicitly, so they can be checked exactly.
	// Metric expressions are SQL and are not parsed here: a false alarm on a
	// working model would teach the customer to ignore this check, which costs
	// more than the misses.
	for i := range eng.Model.Dimensions {
		dim := &eng.Model.Dimensions[i]
		table := tableOf[dim.Entity]
		cols, ok := live[strings.ToLower(table)]
		if !ok {
			continue // already reported as a missing table
		}
		if !cols[strings.ToLower(dim.Column)] {
			d.MissingColumns = append(d.MissingColumns, ColumnRef{
				Entity: dim.Entity, Table: table, Column: dim.Column, UsedBy: dim.Name,
			})
		}
	}

	d.StaleFeeds = staleFeeds(ctx, eng, tableOf, live, staleAfter)

	if set != nil && len(set.Cases) > 0 {
		rep, rerr := nleval.Reconcile(ctx, eng, set)
		if rerr != nil {
			return nil, rerr
		}
		for _, r := range rep.Results {
			if !r.Pass {
				d.Failing = append(d.Failing, FailedCase{
					Metric: r.Metric, Got: r.Got, Want: r.Want, Source: r.Source, Error: r.Error,
				})
			}
		}
	}
	return d, nil
}

// staleFeeds asks every time dimension for its newest value. A source that
// stopped sending leaves each row valid and the totals merely not growing,
// which is the failure nobody notices until a quarter looks flat.
func staleFeeds(ctx context.Context, eng *engine.Engine, tableOf map[string]string,
	live map[string]map[string]bool, staleAfter time.Duration) []StaleFeed {
	var out []StaleFeed
	for i := range eng.Model.Dimensions {
		dim := &eng.Model.Dimensions[i]
		if dim.Type != "time" {
			continue
		}
		table := tableOf[dim.Entity]
		if cols, ok := live[strings.ToLower(table)]; !ok || !cols[strings.ToLower(dim.Column)] {
			continue
		}
		q := eng.Dialect
		res, err := eng.WH.Query(ctx, fmt.Sprintf("SELECT MAX(%s) FROM %s",
			q.QuoteIdent(dim.Column), q.QuoteIdent(table)))
		if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
			continue
		}
		newest, ok := asTime(res.Rows[0][0])
		if !ok {
			continue
		}
		if behind := time.Since(newest); behind > staleAfter {
			out = append(out, StaleFeed{
				Dimension: dim.Name, Table: table, Column: dim.Column,
				Newest: newest.Format("2006-01-02"), Days: int(behind.Hours() / 24),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Days > out[j].Days })
	return out
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		return parseAny(t)
	case []byte:
		return parseAny(string(t))
	default:
		return time.Time{}, false
	}
}

func parseAny(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Summary is the one line a monitor or a CI job prints.
func (d *Drift) Summary() string {
	if d.Clean() {
		return "no drift — the model still matches the warehouse"
	}
	var parts []string
	if n := len(d.MissingTables); n > 0 {
		parts = append(parts, fmt.Sprintf("%d table(s) gone", n))
	}
	if n := len(d.MissingColumns); n > 0 {
		parts = append(parts, fmt.Sprintf("%d column(s) gone", n))
	}
	if n := len(d.StaleFeeds); n > 0 {
		parts = append(parts, fmt.Sprintf("%d feed(s) stopped", n))
	}
	if n := len(d.Failing); n > 0 {
		parts = append(parts, fmt.Sprintf("%d metric(s) no longer match their control", n))
	}
	return "DRIFT — " + strings.Join(parts, ", ")
}

// WriteText renders the findings, each with what to do about it. A monitor that
// says only "drift detected" hands the reader a search problem at the worst
// moment.
func (d *Drift) WriteText(w interface{ Write([]byte) (int, error) }) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	p("%s", d.Summary())
	if d.Clean() {
		return
	}
	for _, t := range d.MissingTables {
		p("  table %s no longer exists — the model's entity points at nothing; find what it was renamed to", t)
	}
	for _, c := range d.MissingColumns {
		p("  %s.%s is gone, used by %s — every query grouping on it now fails", c.Table, c.Column, c.UsedBy)
	}
	for _, s := range d.StaleFeeds {
		p("  %s stops at %s (%d days ago) — totals over it are quietly no longer growing", s.Dimension, s.Newest, s.Days)
	}
	for _, f := range d.Failing {
		if f.Error != "" {
			p("  %s could not be checked: %s", f.Metric, f.Error)
			continue
		}
		p("  %s = %s, its control says %s%s", f.Metric, nleval.Num(f.Got), nleval.Num(f.Want), anchorNote(f.Source))
	}
}

func anchorNote(source string) string {
	if source == "" || source == nleval.SourceEngineer {
		return ""
	}
	// A metric that stopped matching a figure the customer publishes is a
	// different conversation from one that stopped matching a derivation.
	return " (anchored to " + source + ")"
}
