package consult

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

// dayLayout is the window boundary format. Days, not instants: a consulting
// window is "the 30 days before adoption", and a date literal compares
// correctly against date and timestamp columns on every engine DI speaks.
const dayLayout = "2006-01-02"

// TimeDimension picks the time dimension a metric's windows are cut on:
// explicit if given, else the service default if it applies to the metric,
// else the metric's only time dimension. Anything else is refused — without
// a time window the baseline and the acceptance would measure the same
// all-time number, and the loop would judge nothing.
func TimeDimension(m *semantic.Model, metric, explicit, fallback string) (string, error) {
	if m == nil {
		return "", refuse(RuleNoTimeDimension, "没有语义模型")
	}
	name, ok := m.ResolveMetricName(metric)
	if !ok {
		return "", refuse(RuleUnknownMetric, "模型里没有指标 %q", metric)
	}
	reach, err := m.DimensionsFor(name)
	if err != nil {
		return "", refuse(RuleNoTimeDimension, "指标 %q 的可用维度查不出来：%v", metric, err)
	}
	var times []string
	for _, d := range reach {
		if dd := m.Dimension(d); dd != nil && dd.Type == "time" {
			times = append(times, d)
		}
	}
	pick := func(want string) (string, error) {
		if dn, ok := m.ResolveDimensionName(want); ok {
			want = dn
		}
		if !slices.Contains(times, want) {
			return "", refuse(RuleNoTimeDimension, "%q 不是指标 %q 能用的时间维度（能用的：%s）", want, metric, orNone(times))
		}
		return want, nil
	}
	switch {
	case explicit != "":
		return pick(explicit)
	case fallback != "" && slices.Contains(times, fallback):
		return fallback, nil
	case len(times) == 1:
		return times[0], nil
	case len(times) == 0:
		return "", refuse(RuleNoTimeDimension, "指标 %q 没有可用的时间维度 —— 没有时间窗口，基线和验收量的是同一个全时段的数，什么也判不了", metric)
	default:
		return "", refuse(RuleNoTimeDimension, "指标 %q 有多个时间维度（%s），要指定 time_dimension", metric, strings.Join(times, ", "))
	}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "无"
	}
	return strings.Join(s, ", ")
}

// WindowQuery is the semantic query that measures metric over scope and the
// half-open day window [from, to). The window is a filter on the model's time
// dimension — through the compiler, never a hand-written predicate.
func WindowQuery(metric string, scope []semantic.Filter, timeDim string, from, to time.Time) semantic.Query {
	where := append([]semantic.Filter{}, scope...)
	where = append(where,
		semantic.Filter{Dimension: timeDim, Op: ">=", Values: []any{from.Format(dayLayout)}},
		semantic.Filter{Dimension: timeDim, Op: "<", Values: []any{to.Format(dayLayout)}},
	)
	return semantic.Query{Metrics: []string{metric}, Where: where}
}

// Day truncates t to its UTC calendar day.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Querier runs one governed semantic query. In production it is
// governance.Query over the engine — the same path /v1/query takes.
type Querier func(ctx context.Context, q semantic.Query, p governance.Principal) (*engine.Answer, error)

// Governed is the production Querier.
func Governed(eng *engine.Engine, pol governance.Policy) Querier {
	return func(ctx context.Context, q semantic.Query, p governance.Principal) (*engine.Answer, error) {
		return governance.Query(ctx, eng, q, p, pol)
	}
}

// measure runs one window measurement. No rows, a NULL, or an unparseable
// value is nil — never 0.
func measure(ctx context.Context, run Querier, p governance.Principal, metric string, scope []semantic.Filter, timeDim string, from, to time.Time) (*Measurement, error) {
	q := WindowQuery(metric, scope, timeDim, from, to)
	ans, err := run(ctx, q, p)
	if err != nil {
		return nil, fmt.Errorf("量 %s（%s ~ %s）：%w", metric, from.Format(dayLayout), to.Format(dayLayout), err)
	}
	m := &Measurement{
		Metric: metric, Scope: scope, TimeDimension: timeDim,
		From: from.Format(dayLayout), To: to.Format(dayLayout),
		Value: scalar(ans), SQL: ans.SQL, ExecMs: ans.ExecMs, TraceID: ans.TraceID,
		At: time.Now().UTC(),
	}
	// The compiler COALESCEs an empty aggregate to 0, so the scalar alone
	// cannot tell "zero" from "nothing was recorded". Count the days in the
	// window that have rows at all — the same query grouped by day — and treat
	// a window with none as no data. It also tells a reader how much of the
	// window the number stands on.
	cq := q
	cq.GroupBy = []string{timeDim}
	cq.TimeGrain = "day"
	cov, err := run(ctx, cq, p)
	if err != nil {
		return nil, fmt.Errorf("量 %s 的数据覆盖：%w", metric, err)
	}
	m.DataDays = len(cov.Rows)
	if m.DataDays == 0 {
		m.Value = nil
	}
	return m, nil
}

// scalar is the single number of a one-row answer, or nil.
func scalar(a *engine.Answer) *float64 {
	if a == nil || len(a.Rows) != 1 || len(a.Rows[0]) == 0 {
		return nil
	}
	return number(a.Rows[0][len(a.Rows[0])-1])
}

// number converts a driver value to a float; nil for NULL or non-numbers.
// Decimals arrive as strings or bytes on several drivers; they parse, and a
// value that does not parse is nil, not 0.
func number(v any) *float64 {
	var f float64
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		f = t
	case float32:
		f = float64(t)
	case int64:
		f = float64(t)
	case int32:
		f = float64(t)
	case int:
		f = float64(t)
	case []byte:
		return number(string(t))
	case string:
		x, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return nil
		}
		f = x
	case fmt.Stringer:
		return number(t.String())
	default:
		x, err := strconv.ParseFloat(fmt.Sprint(t), 64)
		if err != nil {
			return nil
		}
		f = x
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}
