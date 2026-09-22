package board

import "strings"

// The chart is authored here, from the rows, as a complete ECharts option.
//
// It is not asked of a model for the same reason the numbers are not: a chart
// is a claim about the numbers, and one drawn from data the host never saw can
// disagree with the table beside it without anybody noticing. Drawing it from
// the rows that were just executed means the two cannot diverge.
//
// The shape is chosen rather than configured, because the shape that reads
// correctly follows from what is on the axes:
//
//   - a time grain is a line: the points are ordered and the gaps mean
//     something
//   - one categorical dimension is a bar, sorted by the measure, because the
//     question behind such a panel is always "which is biggest"
//   - a pie only when the caller asks for one AND there are few enough slices
//     to read; a pie of fourteen is a colour wheel
//   - no dimension at all is a single number, which is a table row and needs
//     no chart
//   - more than one dimension is a table: a grouped bar of two dimensions is
//     readable, three is not, and the honest thing is to show the numbers
//
// A panel whose chart would mislead gets no chart. That is a feature: the
// table is still there, and an absent chart is a smaller lie than a bad one.

const maxPieSlices = 8

func chartFor(p Panel, cols []string, rows [][]any) any {
	if strings.EqualFold(p.Chart, "none") || len(rows) == 0 || len(cols) < 2 {
		return nil
	}
	// The first column is the dimension the rows are broken down by; the
	// second is the measure to draw. Further measures stay in the table —
	// two series on one axis with different units is the classic way to make
	// a chart that argues for a conclusion the numbers do not support.
	labels := make([]any, 0, len(rows))
	values := make([]any, 0, len(rows))
	for _, r := range rows {
		if len(r) < 2 {
			return nil
		}
		v, ok := asNumber(r[1])
		if !ok {
			// A measure that is not a number cannot be plotted, and guessing
			// is how a chart ends up drawn from string lengths.
			return nil
		}
		labels = append(labels, r[0])
		values = append(values, v)
	}

	kind := strings.ToLower(strings.TrimSpace(p.Chart))
	if kind == "" {
		switch {
		case p.Grain != "":
			kind = "line"
		case len(p.GroupBy) == 1:
			kind = "bar"
		default:
			return nil
		}
	}

	switch kind {
	case "pie":
		if len(values) > maxPieSlices {
			// Too many slices to read. Fall back to a bar rather than to
			// nothing: the caller wanted a composition and a bar still shows
			// one, where a twenty-slice pie shows a colour wheel.
			kind = "bar"
		} else {
			data := make([]any, 0, len(values))
			for i := range values {
				data = append(data, map[string]any{"name": labels[i], "value": values[i]})
			}
			return map[string]any{
				"tooltip": map[string]any{"trigger": "item"},
				"series": []any{map[string]any{
					"type": "pie", "name": cols[1], "radius": []string{"40%", "70%"}, "data": data,
				}},
			}
		}
	case "bar", "line":
	default:
		return nil
	}

	return map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		"grid":    map[string]any{"left": 8, "right": 16, "top": 24, "bottom": 8, "containLabel": true},
		"xAxis": map[string]any{
			"type": "category", "data": labels,
			// A time axis reads left to right and a category axis needs its
			// labels to fit; rotating only when there are many keeps the
			// common case upright.
			"axisLabel": map[string]any{"rotate": rotationFor(len(labels), p.Grain)},
		},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": kind, "name": cols[1], "data": values, "smooth": kind == "line"}},
	}
}

func rotationFor(n int, grain string) int {
	if grain != "" || n <= 8 {
		return 0
	}
	return 35
}

// asNumber accepts every numeric shape a driver produces.
//
// The first version asserted float64 and nothing else, so a COUNT — which
// arrives as int64 — drew no chart at all: three panels of a twelve-panel
// board came back as bare tables with no explanation, which reads as "this
// metric is unchartable" rather than "the host only handles one Go type".
func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	}
	return 0, false
}
