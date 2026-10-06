package consult

import (
	"context"
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/governance"
)

// ActionRunner carries out a plan's actions. The external-MCP set implements
// it; the consult package never knows what a tool does.
type ActionRunner interface {
	ActionPolicy
	CallAction(ctx context.Context, server, tool string, args map[string]any) (string, error)
}

// Service runs the loop over one governed database.
type Service struct {
	Store   *Store
	Model   *semantic.Model
	Dialect semantic.Dialect
	Query   Querier
	// MeasureAs is the identity every baseline, progress and acceptance
	// measurement runs as. One identity for all of them, so row filters cannot
	// make the baseline and the acceptance measure different rows.
	MeasureAs governance.Principal
	// Actions runs adopted plans' actions; nil means no action may be named.
	Actions ActionRunner
	// TimeDimension is the default window dimension when a metric has several.
	TimeDimension string
	// AdoptRoles, when set, are the only roles that may adopt.
	AdoptRoles []string
	// AllowAsOf lets an operator anchor windows on a past day (replaying
	// history). Every use is recorded on the decision or acceptance.
	AllowAsOf bool
	// Audit, when set, records consult events in the governed audit trail.
	Audit func(ctx context.Context, p governance.Principal, note string, refused bool)
	// Now is the clock; nil is time.Now. Tests set it.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) audit(ctx context.Context, p governance.Principal, refused bool, format string, a ...any) {
	if s.Audit != nil {
		s.Audit(ctx, p, fmt.Sprintf(format, a...), refused)
	}
}

// asOf resolves the anchor day: today, or a past day when replay is allowed.
func (s *Service) asOf(raw string) (time.Time, bool, error) {
	today := Day(s.now())
	if strings.TrimSpace(raw) == "" {
		return today, false, nil
	}
	if !s.AllowAsOf {
		return time.Time{}, false, refuse(RuleAsOfNotAllowed, "这个部署不允许指定 as_of（consult.allow_as_of 没开）")
	}
	t, err := time.Parse(dayLayout, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, false, refuse(RuleAsOfNotAllowed, "as_of 要写成 YYYY-MM-DD：%v", err)
	}
	if t.After(today) {
		return time.Time{}, false, refuse(RuleAsOfNotAllowed, "as_of %s 在未来", raw)
	}
	return t, !t.Equal(today), nil
}

// dryCompile refuses a scope or window the compiler would refuse, before
// anything is stored that could never be measured.
func (s *Service) dryCompile(metric string, scope []semantic.Filter, timeDim string) error {
	if s.Dialect == nil {
		return nil
	}
	d := Day(s.now())
	q := WindowQuery(metric, scope, timeDim, d.AddDate(0, 0, -1), d)
	if _, err := semantic.Compile(s.Model, q, s.Dialect); err != nil {
		return refuse(RuleBadScope, "%s 在这个范围和时间窗口上量不了：%v", metric, err)
	}
	return nil
}

// GoalInput is what a caller supplies for a goal. The baseline is not in it.
type GoalInput struct {
	What          string            `json:"what"`
	Metric        string            `json:"metric"`
	Direction     Direction         `json:"direction"`
	By            float64           `json:"by"`
	WithinDays    int               `json:"within_days"`
	Scope         []semantic.Filter `json:"scope,omitempty"`
	TimeDimension string            `json:"time_dimension,omitempty"`
	Guards        []Guard           `json:"guards,omitempty"`
	AsOf          string            `json:"as_of,omitempty"`
}

// AddGoal records stage one. The goal's baseline is measured here, over the
// trailing window, as MeasureAs.
func (s *Service) AddGoal(ctx context.Context, in GoalInput, who governance.Principal, via string) (*Goal, error) {
	g := &Goal{
		What: strings.TrimSpace(in.What), Metric: in.Metric, Direction: in.Direction, By: in.By,
		WithinDays: in.WithinDays, Scope: in.Scope, Guards: in.Guards,
		Author: who.User, Via: via, At: s.now(),
	}
	if err := CheckGoal(s.Model, g); err != nil {
		return nil, err
	}
	g.Metric, _ = s.Model.ResolveMetricName(g.Metric)
	td, err := TimeDimension(s.Model, g.Metric, in.TimeDimension, s.TimeDimension)
	if err != nil {
		return nil, err
	}
	g.TimeDimension = td
	if err := s.dryCompile(g.Metric, g.Scope, td); err != nil {
		return nil, err
	}
	anchor, _, err := s.asOf(in.AsOf)
	if err != nil {
		return nil, err
	}
	mp := s.measurer(fmt.Sprintf("consult: goal baseline for %q", g.What))
	if g.Baseline, err = measure(ctx, s.Query, mp, g.Metric, g.Scope, td, anchor.AddDate(0, 0, -g.WithinDays), anchor); err != nil {
		return nil, err
	}
	id, err := s.Store.insertWithID(ctx, kGoal, "", who.User, func(id string) any { g.ID = id; return g })
	if err != nil {
		return nil, err
	}
	g.ID = id
	s.audit(ctx, who, false, "consult goal %s recorded: %s", id, g.What)
	return g, nil
}

func (s *Service) measurer(question string) governance.Principal {
	p := s.MeasureAs
	p.Question = question
	return p
}

// EvidenceInput is one query a caller wants run as evidence.
type EvidenceInput struct {
	Asked string         `json:"asked"`
	Query semantic.Query `json:"query"`
}

// maxEvidenceRows bounds what one piece of evidence stores. The row count and
// the SQL are kept in full; the SQL can always be re-run.
const maxEvidenceRows = 200

// RunEvidence executes one query through governance as who, and returns it as
// evidence. Nothing in the result comes from the caller except the question.
func (s *Service) RunEvidence(ctx context.Context, in EvidenceInput, who governance.Principal) (*Evidence, error) {
	if len(in.Query.Metrics) == 0 {
		return nil, refuse(RuleEvidenceFailed, "证据要是一条语义查询（至少一个指标）")
	}
	p := who
	p.Question = "consult evidence: " + in.Asked
	ans, err := s.Query(ctx, in.Query, p)
	if err != nil {
		return nil, refuse(RuleEvidenceFailed, "证据查询没跑成（%s）：%v", in.Asked, err)
	}
	e := &Evidence{
		Asked: in.Asked, Query: in.Query, SQL: ans.SQL, Columns: ans.Columns,
		RowCount: len(ans.Rows), Value: scalar(ans), ExecMs: ans.ExecMs, TraceID: ans.TraceID, At: s.now(),
	}
	e.Rows = ans.Rows
	if len(e.Rows) > maxEvidenceRows {
		e.Rows, e.Truncated = e.Rows[:maxEvidenceRows], true
	}
	return e, nil
}

// FindingInput is a finding as a caller proposes it: a sentence and the
// queries that support it. The service runs the queries.
type FindingInput struct {
	Goal     string          `json:"goal,omitempty"`
	Says     string          `json:"says"`
	Evidence []EvidenceInput `json:"evidence"`
}

// AddFinding records stage three. Each evidence query is executed by the
// server; if any fails, the finding is refused.
func (s *Service) AddFinding(ctx context.Context, in FindingInput, who governance.Principal, via string) (*Finding, error) {
	if in.Goal != "" {
		if _, err := s.Store.Goal(ctx, in.Goal); err != nil {
			return nil, err
		}
	}
	f := &Finding{Goal: in.Goal, Says: strings.TrimSpace(in.Says), By: who.User, Via: via, At: s.now()}
	if len(in.Evidence) == 0 {
		return nil, CheckFinding(f)
	}
	for _, ei := range in.Evidence {
		e, err := s.RunEvidence(ctx, ei, who)
		if err != nil {
			return nil, err
		}
		f.Evidence = append(f.Evidence, *e)
	}
	if err := CheckFinding(f); err != nil {
		return nil, err
	}
	id, err := s.Store.insertWithID(ctx, kFinding, in.Goal, who.User, func(id string) any { f.ID = id; return f })
	if err != nil {
		return nil, err
	}
	f.ID = id
	s.audit(ctx, who, false, "consult finding %s recorded with %d evidence queries", id, len(f.Evidence))
	return f, nil
}

// PlanInput is a plan as proposed.
type PlanInput struct {
	Goal     string   `json:"goal,omitempty"`
	Findings []string `json:"findings"`
	Does     string   `json:"does"`
	Expect   Expect   `json:"expect"`
	Guards   []Guard  `json:"guards,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
	Cost     string   `json:"cost,omitempty"`
}

// ProposePlan records stage four. It does not adopt or run anything.
func (s *Service) ProposePlan(ctx context.Context, in PlanInput, who governance.Principal, via string) (*Plan, error) {
	p := &Plan{
		Goal: in.Goal, Findings: in.Findings, Does: strings.TrimSpace(in.Does), Expect: in.Expect,
		Guards: in.Guards, Actions: in.Actions, Cost: in.Cost, By: who.User, Via: via, At: s.now(),
	}
	if in.Goal != "" {
		g, err := s.Store.Goal(ctx, in.Goal)
		if err != nil {
			return nil, err
		}
		// A plan measured over the whole business while its goal is one line
		// would be judged on other lines' noise. Inherit unless told otherwise.
		if len(p.Expect.Scope) == 0 && len(g.Scope) > 0 {
			p.Expect.Scope = g.Scope
			p.Note = joinNote(p.Note, "范围沿用目标 "+g.ID)
		}
		if p.Expect.TimeDimension == "" && g.Metric == p.Expect.Metric {
			p.Expect.TimeDimension = g.TimeDimension
		}
	}
	known, err := s.Store.findingIDs(ctx)
	if err != nil {
		return nil, err
	}
	var policy ActionPolicy
	if s.Actions != nil {
		policy = s.Actions
	}
	if err := CheckPlan(s.Model, p, known, policy); err != nil {
		return nil, err
	}
	p.Expect.Metric, _ = s.Model.ResolveMetricName(p.Expect.Metric)
	td, err := TimeDimension(s.Model, p.Expect.Metric, p.Expect.TimeDimension, s.TimeDimension)
	if err != nil {
		return nil, err
	}
	p.Expect.TimeDimension = td
	if err := s.dryCompile(p.Expect.Metric, p.Expect.Scope, td); err != nil {
		return nil, err
	}
	for _, g := range p.Guards {
		gtd, err := s.guardTimeDim(g.Metric, td)
		if err != nil {
			return nil, err
		}
		if err := s.dryCompile(g.Metric, p.Expect.Scope, gtd); err != nil {
			return nil, err
		}
	}
	if len(p.Guards) == 0 {
		// Legal, but it means "any cost is acceptable" — make that visible.
		p.Note = joinNote(p.Note, "没有声明护栏 —— 意味着任何代价都接受")
	}
	id, err := s.Store.insertWithID(ctx, kPlan, in.Goal, who.User, func(id string) any { p.ID = id; return p })
	if err != nil {
		return nil, err
	}
	p.ID = id
	s.audit(ctx, who, false, "consult plan %s proposed (%d actions)", id, len(p.Actions))
	return p, nil
}

// guardTimeDim prefers the plan's own time dimension, so a guard is measured
// over exactly the plan's days; a guard metric that cannot reach it uses its own.
func (s *Service) guardTimeDim(metric, planDim string) (string, error) {
	if td, err := TimeDimension(s.Model, metric, planDim, s.TimeDimension); err == nil {
		return td, nil
	}
	return TimeDimension(s.Model, metric, "", s.TimeDimension)
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// AdoptResult is what adoption did.
type AdoptResult struct {
	Decision *Decision   `json:"decision"`
	Actions  []ActionRun `json:"actions"`
}

// Adopt is the human decision. In order: the adopter is checked, the
// baselines are measured over the trailing window and pinned, the decision is
// written (exactly once — a second adoption is refused), and only then are the
// plan's actions run, each call recorded.
func (s *Service) Adopt(ctx context.Context, planID string, who governance.Principal, why, asOf string) (*AdoptResult, error) {
	p, err := s.Store.Plan(ctx, planID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*AdoptResult, error) {
		s.audit(ctx, who, true, "consult adopt %s by %s refused: %v", planID, who.User, err)
		return nil, err
	}
	if err := CheckAdopter(p, who.User, who.Role, s.AdoptRoles); err != nil {
		return fail(err)
	}
	if d, err := s.Store.Decision(ctx, planID); err != nil {
		return nil, err
	} else if d != nil {
		return fail(refuse(RuleAlreadyDecided, "计划 %s 已经被 %s %s 了 —— 一个决定改不了，要改就提一条新的", planID, d.Who, zhVerdict(d.Verdict)))
	}
	anchor, forced, err := s.asOf(asOf)
	if err != nil {
		return fail(err)
	}
	from := anchor.AddDate(0, 0, -p.Expect.WithinDays)
	mp := s.measurer(fmt.Sprintf("consult: baseline for plan %s (adopted by %s)", planID, who.User))
	base, err := measure(ctx, s.Query, mp, p.Expect.Metric, p.Expect.Scope, p.Expect.TimeDimension, from, anchor)
	if err != nil {
		return fail(err)
	}
	if base.Value == nil || *base.Value == 0 {
		return fail(refuse(RuleNoBaseline, "采纳前量不到 %s 在 %s ~ %s 的基线（结果为%s）—— 没有基线的计划永远验收不了，先补数据或换范围",
			p.Expect.Metric, base.From, base.To, nilOrZero(base.Value)))
	}
	d := &Decision{
		Plan: planID, Verdict: Adopted, Who: who.User, Role: who.Role, Why: why, At: s.now(),
		AsOf: anchor.Format(dayLayout), AsOfForced: forced, Baseline: base,
	}
	for _, g := range p.Guards {
		td, err := s.guardTimeDim(g.Metric, p.Expect.TimeDimension)
		if err != nil {
			return fail(err)
		}
		gm, err := measure(ctx, s.Query, mp, g.Metric, p.Expect.Scope, td, from, anchor)
		if err != nil {
			return fail(err)
		}
		d.Guards = append(d.Guards, *gm)
	}
	if err := s.Store.put(ctx, kDecision, planID, planID, who.User, string(Adopted), d); err != nil {
		if err == errDuplicate {
			return fail(refuse(RuleAlreadyDecided, "计划 %s 刚刚已经被别人决定了", planID))
		}
		return nil, err
	}
	s.audit(ctx, who, false, "consult plan %s adopted by %s; baseline %s=%s over %s~%s%s",
		planID, who.User, p.Expect.Metric, human(*base.Value), base.From, base.To, forcedNote(forced))
	runs := s.runActions(ctx, p, who)
	return &AdoptResult{Decision: d, Actions: runs}, nil
}

func forcedNote(forced bool) string {
	if forced {
		return " (as_of overridden)"
	}
	return ""
}

func nilOrZero(v *float64) string {
	if v == nil {
		return "空"
	}
	return " 0"
}

func zhVerdict(v Verdict) string {
	if v == Rejected {
		return "否决"
	}
	return "采纳"
}

// runActions executes the plan's actions in order and records each call. A
// failure stops the rest — later steps may depend on earlier ones — and those
// are recorded as skipped, so "not run" is never confused with "never asked".
func (s *Service) runActions(ctx context.Context, p *Plan, who governance.Principal) []ActionRun {
	var runs []ActionRun
	failed := false
	for i, a := range p.Actions {
		r := ActionRun{Plan: p.ID, Index: i + 1, Server: a.Server, Tool: a.Tool, Args: a.Args, At: s.now()}
		switch {
		case failed:
			r.Skipped, r.Error = true, "前一个动作失败，没有执行"
		case s.Actions == nil:
			r.Error, failed = "没有配置外部动作服务", true
		default:
			t0 := time.Now()
			res, err := s.Actions.CallAction(ctx, a.Server, a.Tool, a.Args)
			r.Ms = time.Since(t0).Milliseconds()
			r.Result = truncate(res, 16<<10)
			if err != nil {
				r.Error, failed = err.Error(), true
			}
		}
		_ = s.Store.put(ctx, kActionRun, fmt.Sprintf("%s#%d", p.ID, i+1), p.ID, who.User, actionState(r), r)
		s.audit(ctx, who, r.Error != "", "consult plan %s action %d %s/%s: %s", p.ID, i+1, a.Server, a.Tool, orOK(r.Error))
		runs = append(runs, r)
	}
	return runs
}

func actionState(r ActionRun) string {
	switch {
	case r.Skipped:
		return "skipped"
	case r.Error != "":
		return "failed"
	}
	return "ok"
}

func orOK(e string) string {
	if e == "" {
		return "ok"
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// Reject records a refusal of the plan. The reason is required: a rejected
// recommendation and why is the cheapest input to the next engagement.
func (s *Service) Reject(ctx context.Context, planID string, who governance.Principal, why string) (*Decision, error) {
	p, err := s.Store.Plan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(why) == "" {
		return nil, refuse(RuleWhyRequired, "否决要写理由")
	}
	if err := CheckAdopter(p, who.User, who.Role, s.AdoptRoles); err != nil {
		return nil, err
	}
	d := &Decision{Plan: planID, Verdict: Rejected, Who: who.User, Role: who.Role, Why: why, At: s.now(), AsOf: Day(s.now()).Format(dayLayout)}
	if err := s.Store.put(ctx, kDecision, planID, planID, who.User, string(Rejected), d); err != nil {
		if err == errDuplicate {
			return nil, refuse(RuleAlreadyDecided, "计划 %s 已经有决定了", planID)
		}
		return nil, err
	}
	s.audit(ctx, who, false, "consult plan %s rejected by %s: %s", planID, who.User, why)
	return d, nil
}

// adopted loads a plan and its adoption, refusing anything not adopted.
func (s *Service) adopted(ctx context.Context, planID string) (*Plan, *Decision, error) {
	p, err := s.Store.Plan(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	d, err := s.Store.Decision(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	if d == nil || d.Verdict != Adopted {
		return nil, nil, refuse(RuleNotAdopted, "计划 %s 还没有被采纳 —— 没被采纳的计划没有可验收的东西", planID)
	}
	return p, d, nil
}

// Due is the day a plan's window closes.
func Due(p *Plan, d *Decision) time.Time {
	a, _ := time.Parse(dayLayout, d.AsOf)
	return a.AddDate(0, 0, p.Expect.WithinDays)
}

// evaluate measures the plan metric and guards over [anchor, end) and judges.
func (s *Service) evaluate(ctx context.Context, p *Plan, d *Decision, end time.Time, final bool, by string) (*Acceptance, error) {
	anchor, _ := time.Parse(dayLayout, d.AsOf)
	kind := "progress"
	if final {
		kind = "acceptance"
	}
	mp := s.measurer(fmt.Sprintf("consult: %s for plan %s", kind, p.ID))
	m, err := measure(ctx, s.Query, mp, p.Expect.Metric, p.Expect.Scope, p.Expect.TimeDimension, anchor, end)
	if err != nil {
		return nil, err
	}
	var guards []GuardResult
	for i, g := range p.Guards {
		td, err := s.guardTimeDim(g.Metric, p.Expect.TimeDimension)
		if err != nil {
			return nil, err
		}
		gm, err := measure(ctx, s.Query, mp, g.Metric, p.Expect.Scope, td, anchor, end)
		if err != nil {
			return nil, err
		}
		var gb *float64
		if i < len(d.Guards) {
			gb = d.Guards[i].Value
		}
		broken, why := guardBroken(g, gb, gm.Value)
		guards = append(guards, GuardResult{Guard: g, Baseline: gb, Measured: gm, Broken: broken, Why: why})
	}
	var base *float64
	if d.Baseline != nil {
		base = d.Baseline.Value
	}
	a := Judge(p, base, m, guards, final, s.now())
	a.By = by
	return &a, nil
}

// Measure is "how is it going": the plan's window from adoption to today (or
// to its end, if closed). It is stored as the latest progress and is never a
// verdict — a partial window of a cumulative metric is not comparable with a
// full baseline window, and the result says so.
func (s *Service) Measure(ctx context.Context, planID string, who governance.Principal) (*Acceptance, error) {
	p, d, err := s.adopted(ctx, planID)
	if err != nil {
		return nil, err
	}
	end := Day(s.now()).AddDate(0, 0, 1) // through today
	if d.AsOfForced {
		// A replayed plan's "today" is its anchor; progress runs to its due day.
		end = Due(p, d)
	}
	due := Due(p, d)
	partial := end.Before(due)
	if !partial {
		end = due
	}
	a, err := s.evaluate(ctx, p, d, end, false, who.User)
	if err != nil {
		return nil, err
	}
	a.Partial = partial
	if partial {
		a.Says += fmt.Sprintf("（窗口还没满：%s 到期，现在只量了 %s ~ %s；累计型指标和完整的基线窗口不可直接比）",
			due.Format(dayLayout), a.Measured.From, a.Measured.To)
	}
	if err := s.Store.replace(ctx, kProgress, planID, planID, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Accept is stage six. It refuses before the window closes, re-measures the
// metric and guards over the plan's window, judges, and stores the result
// exactly once. It takes nobody's word for anything.
func (s *Service) Accept(ctx context.Context, planID string, who governance.Principal, asOf string) (*Acceptance, error) {
	p, d, err := s.adopted(ctx, planID)
	if err != nil {
		return nil, err
	}
	if prev, err := s.Store.Acceptance(ctx, planID); err != nil {
		return nil, err
	} else if prev != nil {
		return nil, refuse(RuleAlreadyAccepted, "计划 %s 已经验收过了（%s）", planID, prev.Outcome)
	}
	today, forced, err := s.asOf(asOf)
	if err != nil {
		return nil, err
	}
	due := Due(p, d)
	// A plan adopted on a replayed day is due relative to that day; real time
	// has long passed. Otherwise "today" must have reached the due day.
	if !d.AsOfForced && today.Before(due) {
		days := int(due.Sub(today).Hours()/24 + 0.5)
		return nil, refuse(RuleTooEarly, "计划 %s 的窗口还没到（%s 到期，还差 %d 天）—— 提前验收拿到的是噪声", planID, due.Format(dayLayout), days)
	}
	a, err := s.evaluate(ctx, p, d, due, true, who.User)
	if err != nil {
		return nil, err
	}
	if forced {
		a.Says += "（验收日由操作者指定为 " + today.Format(dayLayout) + "）"
	}
	if err := s.Store.put(ctx, kAcceptance, planID, planID, who.User, string(a.Outcome), a); err != nil {
		if err == errDuplicate {
			return nil, refuse(RuleAlreadyAccepted, "计划 %s 刚刚已经验收过了", planID)
		}
		return nil, err
	}
	s.audit(ctx, who, false, "consult plan %s accepted: %s", planID, a.Outcome)
	return a, nil
}

// DuePlans lists adopted, unaccepted plans whose window has closed.
func (s *Service) DuePlans(ctx context.Context) ([]string, error) {
	plans, err := s.Store.Plans(ctx)
	if err != nil {
		return nil, err
	}
	today := Day(s.now())
	var out []string
	for i := range plans {
		p := &plans[i]
		d, err := s.Store.Decision(ctx, p.ID)
		if err != nil || d == nil || d.Verdict != Adopted {
			continue
		}
		if a, _ := s.Store.Acceptance(ctx, p.ID); a != nil {
			continue
		}
		if d.AsOfForced || !today.Before(Due(p, d)) {
			out = append(out, p.ID)
		}
	}
	return out, nil
}

// AcceptDue accepts every plan whose window has closed, as the system. It is
// what the background ticker in `di serve` calls.
func (s *Service) AcceptDue(ctx context.Context) ([]*Acceptance, []error) {
	ids, err := s.DuePlans(ctx)
	if err != nil {
		return nil, []error{err}
	}
	var out []*Acceptance
	var errs []error
	sys := s.MeasureAs
	sys.User = "system"
	for _, id := range ids {
		a, err := s.Accept(ctx, id, sys, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		out = append(out, a)
	}
	return out, errs
}
