package consult

import (
	"fmt"
	"slices"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
)

// The checks in this file are pure: they look at the request and the model,
// never at the store or the warehouse, so every refusal can be tested on its
// own. The service calls them before anything is written or executed.

func checkMetric(m *semantic.Model, what, metric string) error {
	if strings.TrimSpace(metric) == "" {
		return refuse(RuleUnknownMetric, "%s 没有指定指标 —— 验收那天没法验", what)
	}
	if m == nil {
		return refuse(RuleUnknownMetric, "没有语义模型，%s 的指标 %q 无从核对", what, metric)
	}
	if _, ok := m.ResolveMetricName(metric); !ok {
		hint := ""
		if s := m.SuggestMetricNames(metric, 3); len(s) > 0 {
			hint = "（是不是 " + strings.Join(s, " / ") + "？）"
		}
		return refuse(RuleUnknownMetric, "%s 指着指标 %q，而模型里没有它%s —— 验收那天没法验", what, metric, hint)
	}
	return nil
}

func checkGuards(m *semantic.Model, what string, gs []Guard) error {
	for i, g := range gs {
		if err := checkMetric(m, fmt.Sprintf("%s 的第 %d 条护栏", what, i+1), g.Metric); err != nil {
			return err
		}
		if !g.WorseIs.valid() {
			return refuse(RuleBadGuard, "护栏 %q 要说明「变差」是 up 还是 down", g.Metric)
		}
		if g.Tolerance == nil && g.Limit == nil {
			return refuse(RuleBadGuard, "护栏 %q 既没有容忍幅度（tolerance）也没有绝对界限（limit）—— 一条不说多差算破的护栏什么都挡不住", g.Metric)
		}
		if g.Tolerance != nil && *g.Tolerance < 0 {
			return refuse(RuleBadGuard, "护栏 %q 的容忍幅度不能是负数", g.Metric)
		}
	}
	return nil
}

func checkScope(m *semantic.Model, what string, scope []semantic.Filter) error {
	for _, f := range scope {
		if f.Metric != "" {
			return refuse(RuleBadScope, "%s 的范围只能落在维度上，不能是指标 %q", what, f.Metric)
		}
		if f.IsGroup() {
			if err := checkScope(m, what, append(append([]semantic.Filter{}, f.And...), f.Or...)); err != nil {
				return err
			}
			continue
		}
		if m != nil && m.Dimension(f.Dimension) == nil {
			if _, ok := m.ResolveDimensionName(f.Dimension); !ok {
				return refuse(RuleBadScope, "%s 的范围用到了模型里没有的维度 %q", what, f.Dimension)
			}
		}
	}
	return nil
}

// CheckGoal refuses a goal that cannot be judged on the day it is due.
func CheckGoal(m *semantic.Model, g *Goal) error {
	if strings.TrimSpace(g.What) == "" {
		return refuse(RuleVagueGoal, "目标要用一句人话说清楚想要什么")
	}
	if err := checkMetric(m, "目标", g.Metric); err != nil {
		return err
	}
	if !g.Direction.valid() || g.By <= 0 || g.WithinDays <= 0 {
		return refuse(RuleVagueGoal, "目标 %q 要有方向（up/down）、幅度（>0）和期限（天数 >0）—— 「提升一下」验收不了", g.What)
	}
	if err := checkScope(m, "目标", g.Scope); err != nil {
		return err
	}
	return checkGuards(m, "目标", g.Guards)
}

// CheckFinding refuses a finding with no evidence. Evidence is only ever built
// by the service from a query it ran, so "has evidence" means "was measured".
func CheckFinding(f *Finding) error {
	if strings.TrimSpace(f.Says) == "" {
		return refuse(RuleNoEvidence, "结论是空的")
	}
	if len(f.Evidence) == 0 {
		return refuse(RuleNoEvidence, "结论 %q 没有绑任何证据 —— 一条没有证据的结论是意见，不是结论", f.Says)
	}
	for i, e := range f.Evidence {
		if strings.TrimSpace(e.SQL) == "" {
			return refuse(RuleNoEvidence, "结论 %q 的第 %d 条证据没有执行过的 SQL", f.Says, i+1)
		}
	}
	return nil
}

// ActionPolicy says which external tools a plan may name as actions.
type ActionPolicy interface {
	// ValidateAction returns an error unless server/tool is configured as an
	// action (not merely a read-only tool).
	ValidateAction(server, tool string) error
}

// CheckPlan refuses a plan that is a guess, or whose effect cannot be judged.
func CheckPlan(m *semantic.Model, p *Plan, known []string, actions ActionPolicy) error {
	if strings.TrimSpace(p.Does) == "" {
		return refuse(RuleVagueExpectation, "计划要说清楚建议做什么")
	}
	if len(p.Findings) == 0 {
		return refuse(RuleNoFinding, "计划 %q 没有绑任何结论 —— 一条没有结论支撑的建议是猜", p.Does)
	}
	for _, f := range p.Findings {
		if !slices.Contains(known, f) {
			return refuse(RuleUnknownFinding, "计划 %q 引用了不存在的结论 %q", p.Does, f)
		}
	}
	e := p.Expect
	if strings.TrimSpace(e.Metric) == "" || !e.Direction.valid() || e.By <= 0 || e.WithinDays <= 0 {
		return refuse(RuleVagueExpectation, "计划 %q 的预期效果说不清 —— 要有指标、方向（up/down）、幅度（>0）和窗口（天数 >0）。「大幅改善」验收不了", p.Does)
	}
	if err := checkMetric(m, "计划的预期效果", e.Metric); err != nil {
		return err
	}
	if err := checkScope(m, "计划", e.Scope); err != nil {
		return err
	}
	if err := checkGuards(m, "计划", p.Guards); err != nil {
		return err
	}
	for i, a := range p.Actions {
		if a.Server == "" || a.Tool == "" {
			return refuse(RuleBadAction, "第 %d 个动作要写明 server 和 tool", i+1)
		}
		if actions == nil {
			return refuse(RuleBadAction, "没有配置任何可执行动作的外部服务，动作 %s/%s 无处可去", a.Server, a.Tool)
		}
		if err := actions.ValidateAction(a.Server, a.Tool); err != nil {
			return refuse(RuleBadAction, "动作 %s/%s 不能用：%v", a.Server, a.Tool, err)
		}
	}
	return nil
}

// CheckAdopter refuses an adoption by the proposer, by nobody, or by a role
// that may not adopt.
func CheckAdopter(p *Plan, who, role string, allowedRoles []string) error {
	if strings.TrimSpace(who) == "" || strings.EqualFold(who, "anon") {
		return refuse(RuleAnonymous, "采纳要一个有名字的人 —— 一次匿名的批准等于没有批准")
	}
	if samePerson(p.By, who) {
		return refuse(RuleSelfApproval, "%s 不能采纳自己提出的计划 %s —— 换一个人", who, p.ID)
	}
	if len(allowedRoles) > 0 && !slices.Contains(allowedRoles, role) {
		return refuse(RuleNotAllowed, "角色 %q 不能采纳计划（允许的角色：%s）", role, strings.Join(allowedRoles, ", "))
	}
	return nil
}
