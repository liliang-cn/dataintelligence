package board

import (
	"context"
	"sort"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
)

// Which metrics a board should open with.
//
// The proposal used to walk the model in file order, and on a real customer
// model that is the wrong order. A generated model puts `batch_count`,
// `cleaning_in_qty_sum` and `energy_meter_coke_t_sum` first, because
// introspection emits a count and a sum per column before anybody writes a
// business metric; the six the engagement exists for — three rival definitions
// of first-pass yield, energy per tonne, melt recovery, defect rate — sit lower
// down and every one of them fell off the end of a twelve-panel board.
//
// So the order is evidence first, shape second:
//
//   - What people asked for. The trail already records every question and the
//     metrics it resolved to. A metric somebody asked for last week belongs on
//     the board whatever it is called, and this needs no heuristic at all.
//   - Then what a person wrote. A metric with a description and synonyms was
//     typed by somebody who cared; `batch_count` was emitted by a program. The
//     tell is not perfect and does not need to be, because it only orders what
//     the trail did not already decide.
//   - Then the model's own order, unchanged, so a model with no trail and no
//     descriptions behaves exactly as it did before.
//
// Ranking is deliberately not asked of a model. A board is the thing people
// stop checking, and "which numbers matter here" is a judgement the customer
// makes — the trail is that judgement, already recorded, and preferring it to a
// guess is the whole point.

// asked reads the trail for metrics people actually asked for, most-asked
// first. A missing or empty trail is not an error: it is a deployment where
// nobody has asked anything yet, and the caller falls back to shape.
func asked(ctx context.Context, eng *engine.Engine) map[string]int {
	if eng == nil || eng.WH == nil {
		return nil
	}
	// The refusal filter is applied in Go, not in SQL.
	//
	// `WHERE refused = false OR refused = 0` reads as portable and is not:
	// Postgres answers `operator does not exist: boolean = integer` and the
	// whole query fails, so this returned nothing and the ranking silently did
	// nothing at all. The engines disagree about how a boolean is spelled and
	// about whether an integer may be compared to one, and the trail's own
	// comment records what that cost the last time. Reading the column and
	// deciding here is the one spelling that works everywhere.
	res, err := eng.WH.Query(ctx, `SELECT metrics, refused FROM _audit`)
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, row := range res.Rows {
		if len(row) < 2 || wasRefused(row[1]) {
			continue
		}
		for _, name := range splitMetricList(text(row[0])) {
			counts[name]++
		}
	}
	return counts
}

// wasRefused reads the trail's boolean column, however this engine spelled it.
func wasRefused(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t == "true" || t == "1" || t == "t"
	case []byte:
		return wasRefused(string(t))
	}
	return false
}

// splitMetricList parses the trail's metric column, which is a Go slice
// printed with %v: "[revenue units]".
func splitMetricList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, f := range strings.Fields(s) {
		if f = strings.Trim(f, `",`); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// handWritten reports whether a metric looks like somebody typed it.
//
// "Has a description or synonyms" was the first test and it is worthless: this
// repository's own generator writes both, so on the model it produces every
// metric looked hand-written and the ordering changed nothing. The tests that
// survive contact with a generated model are the two that name the generator
// rather than guess at care:
//
//   - the marker `modelgen` writes into every description it invents
//   - the aggregate suffixes it appends to every column it finds
//
// A metric that carries neither is one a person named. This is a tie-breaker
// for deployments with no trail to read; where there is a trail, it never gets
// to vote on anything the trail already decided.
func handWritten(mt *semantic.Metric) bool {
	if strings.Contains(mt.Description, generatedMarker) {
		return false
	}
	for _, suffix := range []string{"_count", "_sum", "_avg", "_min", "_max"} {
		if strings.HasSuffix(mt.Name, suffix) {
			return false
		}
	}
	return true
}

// generatedMarker is what modelgen puts in every description it writes, asking
// the reader to check it. Its presence is a fact about where the metric came
// from, not an inference.
const generatedMarker = "auto-generated"

// rankMetrics returns the model's metric names in board order.
func rankMetrics(m *semantic.Model, asked map[string]int) []string {
	type scored struct {
		name  string
		asked int
		hand  bool
		order int
	}
	all := make([]scored, 0, len(m.Metrics))
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		all = append(all, scored{name: mt.Name, asked: asked[mt.Name], hand: handWritten(mt), order: i})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].asked != all[j].asked {
			return all[i].asked > all[j].asked
		}
		if all[i].hand != all[j].hand {
			return all[i].hand
		}
		return all[i].order < all[j].order
	})
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.name
	}
	return out
}

// text flattens a driver's idea of a text column.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}

// ProposeFor is the proposal a deployment should get: the model's shape,
// ordered by what its own users have asked for.
//
// It is the entry point every caller wants; Propose and ProposeRanked stay for
// a caller with no engine, such as a test or a model being linted before it is
// pointed at anything.
func ProposeFor(ctx context.Context, eng *engine.Engine, m *semantic.Model) []Panel {
	return ProposeRanked(m, rankMetrics(m, asked(ctx, eng)))
}
