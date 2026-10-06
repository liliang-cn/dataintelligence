package consult

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// guardBroken judges one guard. A guard that could not be measured counts as
// broken: an unmeasurable guard protects nothing, and calling it "held" would
// let a costly change pass acceptance.
func guardBroken(g Guard, baseline, measured *float64) (bool, string) {
	if measured == nil {
		return true, "量不到 —— 量不出来的护栏按破了算"
	}
	v := *measured
	worse := func(a, b float64) bool { // a is worse than b
		if g.WorseIs == Up {
			return a > b
		}
		return a < b
	}
	if g.Limit != nil && worse(v, *g.Limit) {
		return true, fmt.Sprintf("%s 越过了界限 %s", human(v), human(*g.Limit))
	}
	if g.Tolerance != nil {
		if baseline == nil {
			return true, "采纳时没量到这条护栏的基线，容忍幅度无从核对 —— 按破了算"
		}
		b := *baseline
		line := b + math.Abs(b)**g.Tolerance
		if g.WorseIs == Down {
			line = b - math.Abs(b)**g.Tolerance
		}
		if worse(v, line) {
			return true, fmt.Sprintf("从 %s 变到 %s，超出容忍 %.0f%%（界线 %s）", human(b), human(v), *g.Tolerance*100, human(line))
		}
	}
	return false, ""
}

// Judge decides an acceptance. Pure: where the numbers came from is the
// caller's business; this only decides what they mean, so every edge — exactly
// on target, the wrong direction, a guard broken while the metric hit, no
// data — can be tested without a database.
//
// Comparisons use the raw floats; rounding is for the sentence only, so a
// result sitting just under the line cannot be rounded onto it.
func Judge(p *Plan, baseline *float64, measured *Measurement, guards []GuardResult, final bool, at time.Time) Acceptance {
	a := Acceptance{
		Plan: p.ID, Final: final, Metric: p.Expect.Metric,
		Baseline: baseline, Measured: measured, Guards: guards, At: at,
	}
	if baseline == nil {
		a.Outcome = NoData
		a.Says = fmt.Sprintf("没有数据：采纳时没量到 %s 的基线，无从比较", p.Expect.Metric)
		return a
	}
	t := target(*baseline, p.Expect.Direction, p.Expect.By)
	a.Target = &t
	if measured == nil || measured.Value == nil {
		a.Outcome = NoData
		a.Says = fmt.Sprintf("没有数据：%s 在 %s 的窗口里量不到 —— 不判，因为把它当 0 会说成「跌到零了」",
			p.Expect.Metric, window(measured))
		return a
	}
	v := *measured.Value
	hit := v >= t
	moved := v > *baseline
	if p.Expect.Direction == Down {
		hit = v <= t
		moved = v < *baseline
	}
	var broken []string
	for _, g := range guards {
		if g.Broken {
			broken = append(broken, fmt.Sprintf("%s（%s）", g.Guard.Metric, g.Why))
		}
	}
	pct := "基线是 0，只报绝对值"
	if *baseline != 0 {
		pct = fmt.Sprintf("%+.1f%%", (v-*baseline)/math.Abs(*baseline)*100)
	}
	b, m, tt := human(*baseline), human(v), human(t)
	switch {
	case len(broken) > 0:
		a.Outcome = GuardBroken
		reach := "主指标没达标"
		if hit {
			reach = "主指标达标了"
		}
		a.Says = fmt.Sprintf("护栏破了：%s 从 %s 到 %s（%s），%s，但这几条护栏被换掉了：%s。这不算成功",
			p.Expect.Metric, b, m, pct, reach, strings.Join(broken, "；"))
	case hit:
		a.Outcome = Achieved
		a.Says = fmt.Sprintf("达成：%s 从 %s 到 %s（%s），达标线 %s，护栏都守住了", p.Expect.Metric, b, m, pct, tt)
	default:
		a.Outcome = Missed
		how := "方向对但幅度不够"
		if !moved {
			how = "没动或者反了，期望的是" + p.Expect.Direction.zh()
		}
		a.Says = fmt.Sprintf("没做到：%s 从 %s 到 %s（%s），达标线 %s —— %s", p.Expect.Metric, b, m, pct, tt, how)
	}
	if !final {
		a.Says = "【进度，不是验收结论】" + a.Says
	}
	return a
}

func window(m *Measurement) string {
	if m == nil {
		return "（未量）"
	}
	return m.From + " ~ " + m.To
}

// human renders a number for a sentence: thousands separators, two decimals,
// none for a whole number. Only for text — fields stay float64.
func human(v float64) string {
	r := math.Round(v*100) / 100
	s := fmt.Sprintf("%.2f", r)
	if r == math.Trunc(r) {
		s = fmt.Sprintf("%.0f", r)
	}
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if frac != "" {
		return sign + b.String() + "." + frac
	}
	if sign != "" && b.String() == "0" {
		return "0"
	}
	return sign + b.String()
}
