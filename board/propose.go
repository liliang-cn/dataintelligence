package board

import (
	"sort"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// Propose reads a model and suggests a board for it.
//
// This is the only part of the product that guesses, and it guesses about
// layout — never about a number. The guess is mechanical rather than modelled,
// for the same reason the graph is projected rather than extracted: a
// proposal a person can predict is one they can correct, and a board nobody
// asked for should at least be boring.
//
// The rules, in order:
//
//   - the headline is the first metric in board order, on its own, with no
//     breakdown: a board opens with the number people came for. See rank.go for
//     what decides that order — it is the trail, not the file
//   - a time dimension gets that headline over time, because the second
//     question after "how much" is always "compared to when"
//   - each remaining ungated metric gets one panel, broken down by the first
//     dimension that belongs to the entity it measures — a metric grouped by a
//     dimension from an unrelated entity is a fan-out waiting to happen, and
//     the compiler would refuse it anyway
//   - gated metrics are proposed too. The caller may not be able to read them,
//     and then the panel carries the refusal, which is a more useful board
//     than one that silently hides what exists.
func Propose(m *semantic.Model) []Panel { return ProposeRanked(m, nil) }

// ProposeRanked is Propose with the metric order decided by evidence.
//
// order names the metrics to lead with, most important first; names it does
// not mention keep their position behind the ones it does. Passing nil is
// exactly Propose, so a caller with no trail to read loses nothing.
func ProposeRanked(m *semantic.Model, order []string) []Panel {
	if m == nil || len(m.Metrics) == 0 {
		return nil
	}
	entityOf := map[string]string{}
	for i := range m.Metrics {
		entityOf[m.Metrics[i].Name] = m.Metrics[i].Entity
	}
	// A derived metric has no entity of its own; it inherits the reach of
	// whatever it is built from, and the first operand is the honest guess.
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		if entityOf[mt.Name] != "" {
			continue
		}
		for _, ref := range operands(m, mt) {
			if e := entityOf[ref]; e != "" {
				entityOf[mt.Name] = e
				break
			}
		}
	}

	var timeDim string
	dimsByEntity := map[string][]string{}
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		if d.Mask != "" {
			continue // a column that renders as *** makes a chart of one bar
		}
		if d.Type == "time" {
			if timeDim == "" {
				timeDim = d.Name
			}
			continue
		}
		dimsByEntity[d.Entity] = append(dimsByEntity[d.Entity], d.Name)
	}

	ranked := rank(m, order)
	roles := allRoles(m)

	// The compiler decides what can be shown, not a guess made here.
	//
	// The proposal used to judge reachability itself — two hops along declared
	// joins — and the compiler, which also knows cardinality and what a window
	// metric needs, disagreed. On Meridian five of twelve proposed panels could
	// not be computed at all: four window metrics broken down by a brand, which
	// is not a time dimension, and a ratio grouped across a one-to-many join.
	// The board then printed five compile errors, and the page blamed the
	// reader's role for them. Every panel proposed now compiled first; the
	// check is structural, so it runs with every role the model declares —
	// who may read a panel is decided when it runs, as the reader.
	fits := func(q semantic.Query) bool {
		q.Roles = roles
		_, err := semantic.Compile(m, q, semantic.ANSI{})
		return err == nil
	}

	var panels []Panel
	headDone := false
	for _, name := range ranked {
		if len(panels) >= maxPanels {
			break
		}
		mt := m.Metric(name)
		if mt == nil {
			continue
		}
		// A window metric is a transform over time and means nothing without
		// the time dimension: rolling, delta, cumulative and year-to-date by a
		// brand are not questions.
		if strings.TrimSpace(mt.Window) != "" {
			if q := (semantic.Query{Metrics: []string{name}, GroupBy: []string{timeDim}, TimeGrain: "month"}); timeDim != "" && fits(q) {
				panels = append(panels, Panel{Title: name + " over time", Metrics: q.Metrics, GroupBy: q.GroupBy, Grain: "month", Chart: "line"})
			}
			continue
		}
		if !headDone {
			if !fits(semantic.Query{Metrics: []string{name}}) {
				continue
			}
			headDone = true
			panels = append(panels, Panel{Title: name, Metrics: []string{name}, Chart: "none"})
			if q := (semantic.Query{Metrics: []string{name}, GroupBy: []string{timeDim}, TimeGrain: "month"}); timeDim != "" && fits(q) {
				panels = append(panels, Panel{Title: name + " over time", Metrics: q.Metrics, GroupBy: q.GroupBy, Grain: "month", Chart: "line"})
			}
			continue
		}
		// The first dimension the compiler accepts, trying the ones on the
		// metric's own entity first; a metric no breakdown works for is still
		// worth its headline number.
		var by string
		for _, d := range append(reachable(dimsByEntity, entityOf[name], m), categorical(m)...) {
			if fits(semantic.Query{Metrics: []string{name}, GroupBy: []string{d}}) {
				by = d
				break
			}
		}
		switch {
		case by != "":
			panels = append(panels, Panel{Title: name + " by " + by, Metrics: []string{name}, GroupBy: []string{by}, Limit: 20, Chart: "bar"})
		case fits(semantic.Query{Metrics: []string{name}}):
			panels = append(panels, Panel{Title: name, Metrics: []string{name}, Chart: "none"})
		}
	}
	return panels
}

// categorical is every unmasked categorical dimension, as a fallback order
// for breakdowns the entity's own dimensions could not provide.
func categorical(m *semantic.Model) []string {
	var out []string
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		if d.Type != "time" && d.Mask == "" {
			out = append(out, d.Name)
		}
	}
	return out
}

// allRoles is every role the model declares, for a structural compile check
// that must not be decided by who happens to be asking.
func allRoles(m *semantic.Model) []string {
	seen := map[string]bool{}
	var out []string
	for i := range m.Metrics {
		for _, r := range m.Metrics[i].Roles {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	for i := range m.Dimensions {
		for _, r := range m.Dimensions[i].Roles {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// rank puts the named metrics first, in the order given, and keeps everything
// else in the model's own order behind them. A name that is not in the model
// is ignored rather than refused: the order is a preference, and a stale
// preference should not empty a board.
func rank(m *semantic.Model, order []string) []string {
	in := map[string]bool{}
	for i := range m.Metrics {
		in[m.Metrics[i].Name] = true
	}
	out, seen := make([]string, 0, len(m.Metrics)), map[string]bool{}
	for _, name := range order {
		if in[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for i := range m.Metrics {
		if n := m.Metrics[i].Name; !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// reachable is the dimensions a metric on this entity can be grouped by: its
// own, plus those of the entities it joins to. Declared joins only — the point
// of a declared join graph is that nothing else is a join.
func reachable(byEntity map[string][]string, entity string, m *semantic.Model) []string {
	if entity == "" {
		return nil
	}
	seen := map[string]bool{entity: true}
	frontier := []string{entity}
	for hop := 0; hop < 2 && len(frontier) > 0; hop++ {
		var next []string
		for _, e := range frontier {
			for i := range m.Joins {
				j := &m.Joins[i]
				if j.From == e && !seen[j.To] {
					seen[j.To] = true
					next = append(next, j.To)
				}
			}
		}
		frontier = next
	}
	var out []string
	for e := range seen {
		out = append(out, byEntity[e]...)
	}
	sort.Strings(out)
	return out
}

// operands are the metric names a formula or window metric names.
func operands(m *semantic.Model, mt *semantic.Metric) []string {
	if s := strings.TrimSpace(mt.Of); s != "" {
		return []string{s}
	}
	if strings.TrimSpace(mt.Formula) == "" {
		return nil
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(mt.Formula, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7F)
	}) {
		words[w] = true
	}
	var out []string
	for i := range m.Metrics {
		if n := m.Metrics[i].Name; n != mt.Name && words[n] {
			out = append(out, n)
		}
	}
	return out
}
