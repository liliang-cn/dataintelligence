package reported

import (
	"context"
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

// Cause is one reason a figure is not what was reported.
type Cause string

const (
	// CauseDefinition: the model that computes the figure is not the one
	// that computed the report. Somebody re-signed the口径, or nobody did
	// and it changed anyway — Comparison.Approval says which.
	CauseDefinition Cause = "definition"
	// CauseAccess: the access policy the figure is computed under changed —
	// a row filter, a mask, a k-anonymity floor. Same definition, same
	// warehouse, a different slice of it visible.
	CauseAccess Cause = "access"
	// CauseData: the definition and the policy are the ones reported, and
	// the warehouse holds different rows. Late data, a backfill, a deletion.
	CauseData Cause = "data"
)

// RowStatus is what happened to one row of the report.
type RowStatus string

const (
	Same     RowStatus = "same"
	Moved    RowStatus = "moved"
	OnlyThen RowStatus = "only_then" // reported, and absent now
	OnlyNow  RowStatus = "only_now"  // absent from the report, present now
)

// RowDiff is one row, matched between two runs by its dimension values.
type RowDiff struct {
	// Key is the row's dimension values in group-by order. Empty for a query
	// with no group-by, which has one row and needs no key.
	Key    []string  `json:"key"`
	Status RowStatus `json:"status"`
	Cells  []Cell    `json:"cells"`
}

// Cell is one metric of one row, both ways.
type Cell struct {
	Column string `json:"column"`
	Then   any    `json:"then"`
	Now    any    `json:"now"`
	// Delta is now - then when both are numbers, computed exactly and then
	// rounded to a float64 for display.
	Delta *float64 `json:"delta,omitempty"`
	Moved bool     `json:"moved"`
	// Absent names the side the column does not exist on at all, when a
	// definition added or dropped a metric. It is not a null value.
	Absent string `json:"absent,omitempty"`
}

// Current is what today's figure is computed with.
type Current struct {
	Engine *engine.Engine
	Policy governance.Policy
	// Tolerance is how close two numbers must be to count as the same
	// figure; nil is DefaultTolerance.
	Tolerance *Tolerance
}

// Comparison is a report against today.
type Comparison struct {
	Report *Report    `json:"report"`
	At     time.Time  `json:"at"`
	Causes []Cause    `json:"causes"`
	Rows   []RowDiff  `json:"rows"` // then against now, every row
	Now    Run        `json:"now"`
	Split  *Split     `json:"split,omitempty"`
	Tol    Tolerance  `json:"tolerance"`
	Frame  FrameDelta `json:"frame"`
}

// Run is one execution of the report's query.
type Run struct {
	ModelHash  string   `json:"model_hash"`
	Approval   Approval `json:"approval"`
	PolicyHash string   `json:"policy_hash"`
	Columns    []string `json:"columns,omitempty"`
	Rows       [][]any  `json:"rows,omitempty"`
	SQL        string   `json:"sql,omitempty"`
	// Err is why the query could not run today. A definition that dropped
	// the metric is the common case, and it is an answer, not a failure of
	// the comparison.
	Err string `json:"err,omitempty"`
}

// FrameDelta is whether what the figure is computed under moved, independent
// of whether the figure did. A definition can be re-signed and change nothing
// for this query; the reader should still be told it was re-signed.
type FrameDelta struct {
	DefinitionChanged bool `json:"definition_changed"`
	AccessChanged     bool `json:"access_changed"`
}

// Split is the movement separated by cause.
//
// Replay is the reported definition under the reported policy, run against
// today's warehouse. Data is the report against the replay — the same frame
// on two different days, so every difference is data. Frame is the replay
// against today — the same day under two frames, so every difference is the
// definition or the policy.
//
// Separated is false when the replay could not be run, and Why says why;
// then Data and Frame are empty and the movement in Rows is not attributed
// beyond "the frame changed, and the data may have too".
type Split struct {
	Separated bool      `json:"separated"`
	Why       string    `json:"why,omitempty"`
	Replay    Run       `json:"replay"`
	Data      []RowDiff `json:"data,omitempty"`  // moved rows only
	Frame     []RowDiff `json:"frame,omitempty"` // moved rows only
}

// Compare reruns a frozen report's query now and attributes the difference.
//
// It returns an error only when it cannot read the report or has nothing to
// compute with. A query that no longer runs is recorded in Now.Err, because
// "the definition you reported under can no longer compute this" is exactly
// what somebody asking this question needs to hear.
func (s *Store) Compare(ctx context.Context, name string, cur Current) (*Comparison, error) {
	if cur.Engine == nil || !cur.Engine.Governed() {
		return nil, fmt.Errorf("reported: comparing %s needs an engine with a semantic model", name)
	}
	r, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	tol := DefaultTolerance
	if cur.Tolerance != nil {
		tol = *cur.Tolerance
	}
	c := &Comparison{Report: r, At: s.now().UTC(), Causes: []Cause{}, Tol: tol}
	c.Frame = FrameDelta{
		DefinitionChanged: cur.Engine.ModelHash != r.ModelHash,
		AccessChanged:     policyHash(cur.Policy) != r.PolicyHash,
	}
	c.Now = s.run(ctx, cur.Engine, r, cur.Policy)

	keys := len(r.Query.GroupBy)
	if c.Now.Err == "" {
		c.Rows = diff(keys, r.Columns, r.Rows, c.Now.Columns, c.Now.Rows, tol)
	}
	moved := anyMoved(c.Rows)

	if !c.Frame.DefinitionChanged && !c.Frame.AccessChanged {
		// Same definition, same policy: whatever moved, the warehouse moved
		// it. No replay is needed because today's run is the replay.
		if moved {
			c.Causes = append(c.Causes, CauseData)
		}
		return c, nil
	}

	c.Split = s.split(ctx, cur, r, c.Now, tol)
	switch {
	case c.Split.Separated:
		if len(c.Split.Data) > 0 {
			c.Causes = append(c.Causes, CauseData)
		}
		if len(c.Split.Frame) > 0 || c.Now.Err != "" {
			c.Causes = append(c.Causes, c.frameCauses()...)
		}
	case moved || c.Now.Err != "":
		// The frame changed and the replay could not run, so the frame is
		// all this can name. Data is deliberately NOT listed: listing it
		// would be a guess, and not listing it while Separated is false
		// says the question is open, which is the truth.
		c.Causes = append(c.Causes, c.frameCauses()...)
	}
	return c, nil
}

func (c *Comparison) frameCauses() []Cause {
	var out []Cause
	if c.Frame.DefinitionChanged {
		out = append(out, CauseDefinition)
	}
	if c.Frame.AccessChanged {
		out = append(out, CauseAccess)
	}
	return out
}

// split replays the reported frame on today's data.
func (s *Store) split(ctx context.Context, cur Current, r *Report, now Run, tol Tolerance) *Split {
	sp := &Split{}
	eng := cur.Engine
	if cur.Engine.ModelHash != r.ModelHash {
		if r.Model == "" {
			sp.Why = "the report was frozen without its model, so the old definition cannot be replayed on today's data"
			return sp
		}
		m, err := semantic.Load([]byte(r.Model))
		if err != nil {
			sp.Why = "the reported model no longer loads: " + err.Error()
			return sp
		}
		// The old definition on today's connection. The engine's fields
		// are exported for exactly this: a model is data, and the
		// warehouse handle does not care which one compiled the SQL.
		eng = &engine.Engine{Model: m, WH: cur.Engine.WH, Dialect: cur.Engine.Dialect, ModelHash: r.ModelHash}
	}
	sp.Replay = s.run(ctx, eng, r, r.Policy)
	if sp.Replay.Err != "" {
		sp.Why = "the reported definition could not be replayed on today's data: " + sp.Replay.Err
		return sp
	}
	sp.Separated = true
	keys := len(r.Query.GroupBy)
	sp.Data = movedOnly(diff(keys, r.Columns, r.Rows, sp.Replay.Columns, sp.Replay.Rows, tol))
	if now.Err == "" {
		sp.Frame = movedOnly(diff(keys, sp.Replay.Columns, sp.Replay.Rows, now.Columns, now.Rows, tol))
	}
	return sp
}

// run executes the report's query through governance, as the principal the
// report was computed as. Through governance and not around it: a replay
// that skipped the policy would compare a masked figure with an unmasked
// one and call the mask a change of data.
func (s *Store) run(ctx context.Context, eng *engine.Engine, r *Report, pol governance.Policy) Run {
	run := Run{ModelHash: eng.ModelHash, PolicyHash: policyHash(pol), Approval: s.approval(ctx, eng.ModelHash)}
	who := r.Who
	who.Question = "reported: compare " + r.Name
	ans, err := governance.Query(ctx, eng, r.Query, who, pol)
	if err != nil {
		run.Err = err.Error()
		return run
	}
	run.Columns, run.Rows, run.SQL = ans.Columns, ans.Rows, ans.SQL
	return run
}

// diff matches two result sets row by row.
//
// The first `keys` columns are the dimensions — the compiler emits them in
// group-by order before any metric — and a row is matched by their values,
// not by position: a region that disappeared must not shift every row after
// it onto the wrong neighbour. Metric columns are matched by name, so a
// definition that reordered or added a metric is still compared metric to
// metric.
//
// Nothing is dropped. This is an outer join by construction: every row of
// either side appears once, and an unmatched one says which side it is on.
// A join that silently lost rows was a real defect found the same week this
// was written, and a report comparison that lost the one region that
// vanished would be that defect with a signature on it.
//
// Two rows with the same key — a masked dimension collapses every value to
// the same text — are matched in order of appearance.
func diff(keys int, thenCols []string, thenRows [][]any, nowCols []string, nowRows [][]any, tol Tolerance) []RowDiff {
	if keys > len(thenCols) {
		keys = len(thenCols)
	}
	nk := keys
	if nk > len(nowCols) {
		nk = len(nowCols)
	}
	metrics := metricColumns(keys, thenCols, nowCols)
	ti, ni := index(thenCols), index(nowCols)

	type slot struct {
		row  []any
		used bool
	}
	nowBy := map[string][]*slot{}
	var nowOrder []string
	for _, row := range nowRows {
		k := rowKey(row, nk)
		if _, ok := nowBy[k]; !ok {
			nowOrder = append(nowOrder, k)
		}
		nowBy[k] = append(nowBy[k], &slot{row: row})
	}

	var out []RowDiff
	for _, row := range thenRows {
		k := rowKey(row, keys)
		d := RowDiff{Key: keyText(row, keys)}
		var match *slot
		for _, sl := range nowBy[k] {
			if !sl.used {
				match, sl.used = sl, true
				break
			}
		}
		if match == nil {
			d.Status = OnlyThen
			for _, m := range metrics {
				d.Cells = append(d.Cells, Cell{Column: m, Then: cellAt(row, ti, m), Absent: "now"})
			}
			out = append(out, d)
			continue
		}
		d.Status = Same
		for _, m := range metrics {
			cell := Cell{Column: m, Then: cellAt(row, ti, m), Now: cellAt(match.row, ni, m)}
			_, inThen := ti[m]
			_, inNow := ni[m]
			switch {
			case !inThen:
				cell.Absent, cell.Moved = "then", true
			case !inNow:
				cell.Absent, cell.Moved = "now", true
			default:
				eq, delta := tol.same(cell.Then, cell.Now)
				cell.Delta, cell.Moved = delta, !eq
			}
			if cell.Moved {
				d.Status = Moved
			}
			d.Cells = append(d.Cells, cell)
		}
		out = append(out, d)
	}
	for _, k := range nowOrder {
		for _, sl := range nowBy[k] {
			if sl.used {
				continue
			}
			d := RowDiff{Key: keyText(sl.row, nk), Status: OnlyNow}
			for _, m := range metrics {
				d.Cells = append(d.Cells, Cell{Column: m, Now: cellAt(sl.row, ni, m), Absent: "then"})
			}
			out = append(out, d)
		}
	}
	return out
}

func metricColumns(keys int, thenCols, nowCols []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(cols []string) {
		for i, c := range cols {
			if i < keys || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	add(thenCols)
	add(nowCols)
	return out
}

func index(cols []string) map[string]int {
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		m[c] = i
	}
	return m
}

func cellAt(row []any, idx map[string]int, col string) any {
	if i, ok := idx[col]; ok && i < len(row) {
		return row[i]
	}
	return nil
}

func keyText(row []any, keys int) []string {
	out := make([]string, 0, keys)
	for i := 0; i < keys && i < len(row); i++ {
		out = append(out, display(row[i]))
	}
	return out
}

func rowKey(row []any, keys int) string {
	parts := make([]string, 0, keys)
	for i := 0; i < keys && i < len(row); i++ {
		parts = append(parts, text(row[i]))
	}
	return strings.Join(parts, "\x1f")
}

func anyMoved(rows []RowDiff) bool {
	for _, r := range rows {
		if r.Status != Same {
			return true
		}
	}
	return false
}

func movedOnly(rows []RowDiff) []RowDiff {
	var out []RowDiff
	for _, r := range rows {
		if r.Status != Same {
			out = append(out, r)
		}
	}
	return out
}
