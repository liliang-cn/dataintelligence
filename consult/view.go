package consult

import (
	"context"
	"time"
)

// PlanView is one plan with everything that happened to it.
type PlanView struct {
	Plan       Plan        `json:"plan"`
	State      string      `json:"state"` // proposed | adopted | rejected | accepted
	Decision   *Decision   `json:"decision,omitempty"`
	Due        string      `json:"due,omitempty"`
	Actions    []ActionRun `json:"actions,omitempty"`
	Progress   *Acceptance `json:"progress,omitempty"`
	Acceptance *Acceptance `json:"acceptance,omitempty"`
}

// GoalView is one goal with its findings and plans.
type GoalView struct {
	Goal     Goal       `json:"goal"`
	Target   *float64   `json:"target,omitempty"`
	Findings []Finding  `json:"findings"`
	Plans    []PlanView `json:"plans"`
}

// Board is the whole engagement. Findings and plans not tied to a goal are
// listed under Loose rather than dropped.
type Board struct {
	Goals         []GoalView `json:"goals"`
	LooseFindings []Finding  `json:"loose_findings,omitempty"`
	LoosePlans    []PlanView `json:"loose_plans,omitempty"`
	At            time.Time  `json:"at"`
}

// PlanView assembles one plan.
func (s *Service) PlanView(ctx context.Context, id string) (*PlanView, error) {
	p, err := s.Store.Plan(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.planView(ctx, *p)
}

func (s *Service) planView(ctx context.Context, p Plan) (*PlanView, error) {
	v := &PlanView{Plan: p, State: "proposed"}
	var err error
	if v.Decision, err = s.Store.Decision(ctx, p.ID); err != nil {
		return nil, err
	}
	if v.Decision != nil {
		v.State = string(v.Decision.Verdict)
		if v.Decision.Verdict == Adopted {
			v.Due = Due(&p, v.Decision).Format(dayLayout)
		}
	}
	if v.Actions, err = s.Store.ActionRuns(ctx, p.ID); err != nil {
		return nil, err
	}
	if v.Progress, err = s.Store.Progress(ctx, p.ID); err != nil {
		return nil, err
	}
	if v.Acceptance, err = s.Store.Acceptance(ctx, p.ID); err != nil {
		return nil, err
	}
	if v.Acceptance != nil {
		v.State = "accepted"
	}
	return v, nil
}

// GoalView assembles one goal.
func (s *Service) GoalView(ctx context.Context, id string) (*GoalView, error) {
	b, err := s.Board(ctx)
	if err != nil {
		return nil, err
	}
	for i := range b.Goals {
		if b.Goals[i].Goal.ID == id {
			return &b.Goals[i], nil
		}
	}
	return nil, refuse(RuleNotFound, "没有目标 %q", id)
}

// Board assembles the engagement. It reads the ledger only; no query runs.
func (s *Service) Board(ctx context.Context) (*Board, error) {
	goals, err := s.Store.Goals(ctx)
	if err != nil {
		return nil, err
	}
	findings, err := s.Store.Findings(ctx)
	if err != nil {
		return nil, err
	}
	plans, err := s.Store.Plans(ctx)
	if err != nil {
		return nil, err
	}
	b := &Board{Goals: []GoalView{}, At: s.now()}
	idx := map[string]int{}
	for i, g := range goals {
		idx[g.ID] = i
		b.Goals = append(b.Goals, GoalView{Goal: g, Target: goals[i].Target(), Findings: []Finding{}, Plans: []PlanView{}})
	}
	for _, f := range findings {
		if i, ok := idx[f.Goal]; ok {
			b.Goals[i].Findings = append(b.Goals[i].Findings, f)
		} else {
			b.LooseFindings = append(b.LooseFindings, f)
		}
	}
	for _, p := range plans {
		v, err := s.planView(ctx, p)
		if err != nil {
			return nil, err
		}
		if i, ok := idx[p.Goal]; ok {
			b.Goals[i].Plans = append(b.Goals[i].Plans, *v)
		} else {
			b.LoosePlans = append(b.LoosePlans, *v)
		}
	}
	return b, nil
}
