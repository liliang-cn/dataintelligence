// Package anchor finds which scope of a metric reproduces a figure the customer
// already publishes.
//
// This closes the hole in the whole verification story. `di eval` checks a
// metric against a control query, and when the same person wrote both, from the
// same misunderstanding, they agree — and agreement is not correctness. The only
// thing that can settle it is a number the customer produced without us: the
// figure on page four of the quality report, the total in the finance system.
//
// The customer rarely knows how their own number was scoped, though. "一次合格率
// 94.2%" is one quarter, or one plant, or both, and asking which usually gets an
// answer that is wrong in a way nobody notices. So the search runs the other
// way: enumerate the scopes the model can express, and report which ones produce
// the number. The answer is a caption — "your 94.2% is this metric, restricted
// to Q2 2026, at the Changchun plant" — and the caption is the finding.
package anchor

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/engine"
	semantic "github.com/liliang-cn/semantic-go"
)

// Candidate is one scope of the metric that reproduces the published figure.
type Candidate struct {
	Label string            `json:"label"` // human-readable scope
	Where []semantic.Filter `json:"where"`
	Value float64           `json:"value"`
}

// Result is the whole search.
type Result struct {
	Metric  string      `json:"metric"`
	Target  float64     `json:"target"`
	Matches []Candidate `json:"matches"`
	// Closest is reported when nothing matched: the nearest scope found, so the
	// engineer can see whether they are one filter away or in the wrong metric
	// entirely.
	Closest  *Candidate `json:"closest,omitempty"`
	Searched int        `json:"scopes_searched"`
	Queries  int        `json:"queries"`
	Skipped  []string   `json:"skipped,omitempty"`

	Tol float64 `json:"tolerance"`
	// Scale is set when the figure only matches after a unit conversion: the
	// customer publishes 97.3 and the metric is the fraction 0.973, or the
	// figure is in thousands. Percent-versus-fraction is the single commonest
	// reconciliation failure and it looks exactly like a wrong model, so the
	// search says which it is rather than leaving the engineer to wonder.
	Scale float64 `json:"scale,omitempty"`
	// Lo and Hi are the range the metric takes across every scope searched.
	// When the tolerance is wider than that range, everything matches, and the
	// report has to say so: the search did not find the customer's scope, it
	// found that the figure is too coarse to identify one.
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// TooCoarse reports that the published figure cannot distinguish between scopes
// because the metric barely varies across them.
//
// This is the failure that looks most like success. Ninety-seven point three
// per cent matched seventy-five scopes not because the model is ambiguous but
// because every scope of this metric lands between 96.8% and 97.3%, and one
// decimal place cannot tell them apart. The answer is not a tighter search; it
// is to ask the customer for more digits, or to anchor on a metric that moves.
func (r *Result) TooCoarse() bool {
	return len(r.Matches) > 1 && r.Hi-r.Lo <= 2*r.Tol
}

// Options bound the search.
type Options struct {
	// Tol is the absolute tolerance. Zero means "read it off the figure": a
	// customer who writes 97.3% has told you the precision they have, and the
	// right window is half of the last digit. A fixed default is wrong in both
	// directions — too tight and the real scope never appears, too loose and
	// everything matches, which is the same as nothing matching but reads like
	// success.
	Tol float64
	// MaxRowsPerScope caps how many groups a single grouped query may return.
	// A dimension with ten thousand values is not how anyone scopes a headline
	// figure, and enumerating it is a scan the customer feels.
	MaxRowsPerScope int
	// MaxQueries stops the search from becoming the expensive thing.
	MaxQueries int
	// Grains are the time windows tried. Nobody publishes a headline figure by
	// day, and including it multiplies the search by three hundred.
	Grains []string
}

func (o *Options) withDefaults() {
	if o.Tol <= 0 {
		o.Tol = 1e-9
	}
	if o.MaxRowsPerScope <= 0 {
		o.MaxRowsPerScope = 200
	}
	if o.MaxQueries <= 0 {
		o.MaxQueries = 120
	}
	if len(o.Grains) == 0 {
		o.Grains = []string{"year", "quarter", "month"}
	}
}

// Search looks for scopes of metric whose value equals target.
//
// The search is over grouped queries, not over periods: asking for the metric
// grouped by quarter returns every quarter in one pass, and each returned row is
// a candidate. That makes the cost proportional to the number of dimensions
// rather than to the number of periods in the warehouse, which is the difference
// between a search that runs in seconds and one nobody waits for.
func Search(ctx context.Context, eng *engine.Engine, metric string, target float64, opts Options) (*Result, error) {
	opts.withDefaults()
	if eng.Model == nil {
		return nil, fmt.Errorf("anchoring needs a semantic model")
	}
	if eng.Model.Metric(metric) == nil {
		return nil, fmt.Errorf("unknown metric %q", metric)
	}

	r := &Result{Metric: metric, Target: target, Tol: opts.Tol}
	s := &search{eng: eng, metric: metric, target: target, opts: opts, res: r}

	var times, cats []semantic.Dimension
	for _, d := range eng.Model.Dimensions {
		if d.Type == "time" {
			times = append(times, d)
		} else {
			cats = append(cats, d)
		}
	}

	// Whole warehouse first. If the customer's figure is simply the total, every
	// further query is wasted and the answer is the least surprising one.
	s.try(ctx, nil, nil, "")

	for _, t := range times {
		for _, g := range opts.Grains {
			s.try(ctx, nil, &t, g)
		}
	}
	for i := range cats {
		s.try(ctx, &cats[i], nil, "")
	}
	// Segment × period last: it is the largest part of the space and the least
	// likely shape for a headline figure, so it only runs if nothing simpler fit.
	if len(r.Matches) == 0 {
		for i := range cats {
			for j := range times {
				for _, g := range opts.Grains {
					s.try(ctx, &cats[i], &times[j], g)
				}
			}
		}
	}

	// Nothing matched: before reporting that, check whether the figure is the
	// same number in different units.
	if len(r.Matches) == 0 {
		for _, scale := range []float64{0.01, 100, 0.001, 1000} {
			alt := &search{eng: eng, metric: metric, target: target * scale,
				opts: Options{Tol: opts.Tol * math.Abs(scale), MaxRowsPerScope: opts.MaxRowsPerScope,
					MaxQueries: opts.MaxQueries, Grains: opts.Grains},
				res: &Result{Metric: metric, Target: target * scale, Tol: opts.Tol * math.Abs(scale)}}
			alt.try(ctx, nil, nil, "")
			for _, t := range times {
				for _, g := range opts.Grains {
					alt.try(ctx, nil, &t, g)
				}
			}
			for i := range cats {
				alt.try(ctx, &cats[i], nil, "")
			}
			if len(alt.res.Matches) > 0 {
				r.Matches, r.Scale = alt.res.Matches, scale
				r.Lo, r.Hi, r.Tol = alt.res.Lo, alt.res.Hi, alt.res.Tol
				r.Queries += alt.res.Queries
				break
			}
		}
	}

	sort.Slice(r.Matches, func(i, j int) bool { return len(r.Matches[i].Where) < len(r.Matches[j].Where) })
	return r, nil
}

type search struct {
	eng    *engine.Engine
	metric string
	target float64
	opts   Options
	res    *Result
}

// try runs one grouped query and turns each returned row into a candidate.
func (s *search) try(ctx context.Context, cat *semantic.Dimension, tim *semantic.Dimension, grain string) {
	if s.res.Queries >= s.opts.MaxQueries {
		return
	}
	q := semantic.Query{Metrics: []string{s.metric}, TimeGrain: grain}
	if cat != nil {
		q.GroupBy = append(q.GroupBy, cat.Name)
	}
	if tim != nil {
		q.GroupBy = append(q.GroupBy, tim.Name)
	}

	s.res.Queries++
	ans, err := s.eng.Query(ctx, q)
	if err != nil {
		// A dimension the metric cannot reach is not an error in the search; it
		// is a join the model does not have. Recording it keeps the report from
		// implying the scope was tried and ruled out.
		s.res.Skipped = append(s.res.Skipped, fmt.Sprintf("%s: %v", label(cat, tim, grain), err))
		return
	}
	if len(ans.Rows) > s.opts.MaxRowsPerScope {
		s.res.Skipped = append(s.res.Skipped, fmt.Sprintf(
			"%s: %d groups, over the %d cap — nobody scopes a headline figure this finely",
			label(cat, tim, grain), len(ans.Rows), s.opts.MaxRowsPerScope))
		return
	}

	mi := indexOf(ans.Columns, s.metric)
	if mi < 0 {
		return
	}
	for _, row := range ans.Rows {
		s.res.Searched++
		v, ok := asFloat(row[mi])
		if !ok {
			continue
		}
		c := Candidate{Value: v}
		if cat != nil {
			if i := indexOf(ans.Columns, cat.Name); i >= 0 {
				val := fmt.Sprintf("%v", row[i])
				c.Where = append(c.Where, semantic.Filter{Dimension: cat.Name, Op: "=", Values: []any{val}})
			}
		}
		if tim != nil {
			if i := indexOf(ans.Columns, tim.Name); i >= 0 {
				from, to, ok := window(row[i], grain)
				if !ok {
					continue
				}
				// Two half-open bounds rather than an equality on a truncated
				// value: the recon file has to be readable a year later by
				// somebody who was not here, and ">= 2026-04-01 and < 2026-07-01"
				// says what "= Q2" only implies.
				c.Where = append(c.Where,
					semantic.Filter{Dimension: tim.Name, Op: ">=", Values: []any{from}},
					semantic.Filter{Dimension: tim.Name, Op: "<", Values: []any{to}})
			}
		}
		c.Label = describe(c.Where)
		s.record(c)
	}
}

func (s *search) record(c Candidate) {
	if s.res.Searched == 1 || c.Value < s.res.Lo {
		s.res.Lo = c.Value
	}
	if s.res.Searched == 1 || c.Value > s.res.Hi {
		s.res.Hi = c.Value
	}
	if math.Abs(c.Value-s.target) <= s.opts.Tol {
		s.res.Matches = append(s.res.Matches, c)
		return
	}
	if s.res.Closest == nil || math.Abs(c.Value-s.target) < math.Abs(s.res.Closest.Value-s.target) {
		cp := c
		s.res.Closest = &cp
	}
}

// window turns a grouped period value into its half-open bounds.
func window(v any, grain string) (string, string, bool) {
	t, ok := asTime(v)
	if !ok {
		return "", "", false
	}
	var end time.Time
	switch grain {
	case "year":
		end = t.AddDate(1, 0, 0)
	case "quarter":
		end = t.AddDate(0, 3, 0)
	case "month":
		end = t.AddDate(0, 1, 0)
	case "week":
		end = t.AddDate(0, 0, 7)
	default:
		end = t.AddDate(0, 0, 1)
	}
	return t.Format("2006-01-02"), end.Format("2006-01-02"), true
}

func describe(where []semantic.Filter) string {
	if len(where) == 0 {
		return "the whole warehouse, unfiltered"
	}
	parts := make([]string, 0, len(where))
	for _, f := range where {
		vals := make([]string, 0, len(f.Values))
		for _, v := range f.Values {
			vals = append(vals, fmt.Sprintf("%v", v))
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", f.Dimension, f.Op, strings.Join(vals, ", ")))
	}
	return strings.Join(parts, " and ")
}

func label(cat, tim *semantic.Dimension, grain string) string {
	var parts []string
	if cat != nil {
		parts = append(parts, cat.Name)
	}
	if tim != nil {
		parts = append(parts, tim.Name+" by "+grain)
	}
	if len(parts) == 0 {
		return "unfiltered"
	}
	return strings.Join(parts, " × ")
}

func indexOf(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

// ToleranceOf reads the precision a written figure declares.
//
// "97.3" says three significant decimals were known, so the true value lies
// within half of the last digit. Taking that literally is the whole point: it
// is the customer's own statement of how precisely they know their number, and
// it is strictly better than any default this code could pick.
func ToleranceOf(written string) float64 {
	written = strings.TrimSpace(written)
	if i := strings.IndexAny(written, "eE"); i >= 0 {
		written = written[:i]
	}
	dot := strings.IndexByte(written, '.')
	if dot < 0 {
		return 0.5 // "94" means somewhere in [93.5, 94.5)
	}
	decimals := len(written) - dot - 1
	return 0.5 * math.Pow(10, -float64(decimals))
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case []byte:
		return parseFloat(string(n))
	case string:
		return parseFloat(n)
	default:
		return 0, false
	}
}

func parseFloat(s string) (float64, bool) {
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f); err != nil {
		return 0, false
	}
	return f, true
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		return parseTime(t)
	case []byte:
		return parseTime(string(t))
	default:
		return time.Time{}, false
	}
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05",
		"2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
