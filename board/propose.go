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
//   - the headline is whatever the model puts first, on its own, with no
//     breakdown: a board opens with the number people came for
//   - a time dimension gets that headline over time, because the second
//     question after "how much" is always "compared to when"
//   - each remaining ungated metric gets one panel, broken down by the first
//     dimension that belongs to the entity it measures — a metric grouped by a
//     dimension from an unrelated entity is a fan-out waiting to happen, and
//     the compiler would refuse it anyway
//   - gated metrics are proposed too. The caller may not be able to read them,
//     and then the panel carries the refusal, which is a more useful board
//     than one that silently hides what exists.
func Propose(m *semantic.Model) []Panel {
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

	head := m.Metrics[0].Name
	panels := []Panel{{Title: head, Metrics: []string{head}, Chart: "none"}}
	if timeDim != "" {
		panels = append(panels, Panel{
			Title: head + " over time", Metrics: []string{head},
			GroupBy: []string{timeDim}, Grain: "month", Chart: "line",
		})
	}

	for i := range m.Metrics {
		if len(panels) >= maxPanels {
			break
		}
		mt := &m.Metrics[i]
		if mt.Name == head {
			continue
		}
		dims := reachable(dimsByEntity, entityOf[mt.Name], m)
		if len(dims) == 0 {
			panels = append(panels, Panel{Title: mt.Name, Metrics: []string{mt.Name}, Chart: "none"})
			continue
		}
		panels = append(panels, Panel{
			Title:   mt.Name + " by " + dims[0],
			Metrics: []string{mt.Name}, GroupBy: []string{dims[0]},
			Limit: 20, Chart: "bar",
		})
	}
	return panels
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
