// Package consult is the consulting loop as data: 目标 → 调查 → 结论 → 计划 →
// 实施 → 验收 (goal → investigation → finding → plan → execution → acceptance).
//
// Everything else in this repository makes sure a number is right. This
// package makes sure a piece of advice can be walked end to end and checked
// afterwards: what the customer wanted, what the investigation actually showed,
// what was recommended on the strength of it, who agreed to it, what was done,
// and whether it worked — measured, not reported.
//
// # Every stage is a reason to refuse
//
//	| rule                                                      | what it stops                                   |
//	|-----------------------------------------------------------|-------------------------------------------------|
//	| a goal's metric must exist in the semantic model          | a goal nobody can measure on the day it is due   |
//	| a finding binds ≥1 piece of evidence the server executed  | an opinion stored as a finding                   |
//	| a plan binds ≥1 finding                                   | a guess stored as a recommendation               |
//	| the expected effect has metric + direction + size + window| "improve significantly", which cannot be judged  |
//	| the adopter is not the proposer                           | approving your own proposal                      |
//	| the baseline is measured at adoption and pinned           | a baseline remembered, or re-run under new rules |
//	| acceptance re-queries the metric                          | a self-graded acceptance                         |
//	| a broken guard is not a success, even if the metric hit   | "revenue +20%, stock-outs doubled"               |
//	| no data is nil, never 0                                   | "fell to zero" when it was "could not measure"   |
//
// # Numbers come from the server
//
// Evidence is a semantic query the server compiled and ran through
// governance.Query — the same RBAC, row filters, audit and SQL a customer can
// re-run. The stored figure is the warehouse's answer, never a number a model
// or a person typed in. Measurements (baseline, progress, acceptance) go the
// same way: a metric, a scope (dimension filters) and a time window (a filter
// on the model's time dimension), compiled and executed, the SQL kept.
//
// The package is domain-neutral: metrics, dimensions and actions are names from
// the deployment's model and configuration.
package consult

import (
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"
)

// Direction is which way a metric should move.
type Direction string

const (
	Up   Direction = "up"   // bigger is better
	Down Direction = "down" // smaller is better
)

func (d Direction) valid() bool { return d == Up || d == Down }

func (d Direction) zh() string {
	if d == Down {
		return "下降"
	}
	return "上升"
}

// Goal is stage one: what the customer wants, in a shape that can be judged.
type Goal struct {
	ID   string `json:"id"`
	What string `json:"what"` // one sentence, for the customer
	// Metric must exist in the semantic model.
	Metric    string    `json:"metric"`
	Direction Direction `json:"direction"`
	// By is the relative magnitude: 0.1 = 10%.
	By         float64 `json:"by"`
	WithinDays int     `json:"within_days"`
	// Scope narrows the goal to a slice of the business (a line, a plant, a
	// store). Empty means the whole business, which is rarely what anyone means.
	Scope []semantic.Filter `json:"scope,omitempty"`
	// TimeDimension is the model dimension windows are cut on. Empty means the
	// service default or the metric's only time dimension.
	TimeDimension string  `json:"time_dimension,omitempty"`
	Guards        []Guard `json:"guards,omitempty"`
	// Baseline is measured by the server when the goal is recorded, over the
	// trailing WithinDays — never typed in.
	Baseline *Measurement `json:"baseline,omitempty"`
	Author   string       `json:"author"`
	Via      string       `json:"via,omitempty"` // "copilot" when an agent recorded it
	At       time.Time    `json:"at"`
}

// Target is the line the goal must reach, or nil without a baseline.
func (g *Goal) Target() *float64 {
	if g.Baseline == nil || g.Baseline.Value == nil {
		return nil
	}
	t := target(*g.Baseline.Value, g.Direction, g.By)
	return &t
}

// Guard is what may not be traded for the goal.
//
// A guard is broken when its metric moves the bad way by more than Tolerance
// (relative to its own baseline at adoption), or past Limit (absolute). At least
// one of the two must be set; both may be.
type Guard struct {
	Metric    string    `json:"metric"`
	WorseIs   Direction `json:"worse_is"`
	Tolerance *float64  `json:"tolerance,omitempty"` // 0.05 = may get 5% worse than its baseline
	Limit     *float64  `json:"limit,omitempty"`     // absolute bound it may not cross
}

// Evidence is one semantic query the server executed. Nothing here is supplied
// by the caller except Asked and Query.
type Evidence struct {
	Asked    string         `json:"asked"`
	Query    semantic.Query `json:"query"`
	SQL      string         `json:"sql"`
	Columns  []string       `json:"columns"`
	Rows     [][]any        `json:"rows"`
	RowCount int            `json:"row_count"`
	// Truncated is set when more rows came back than are stored.
	Truncated bool      `json:"truncated,omitempty"`
	Value     *float64  `json:"value,omitempty"` // the scalar, when the answer is one number
	ExecMs    int64     `json:"exec_ms"`
	TraceID   string    `json:"trace_id,omitempty"`
	At        time.Time `json:"at"`
}

// Finding is stage three: one sentence the investigation supports.
type Finding struct {
	ID       string     `json:"id"`
	Goal     string     `json:"goal,omitempty"`
	Says     string     `json:"says"`
	Evidence []Evidence `json:"evidence"`
	By       string     `json:"by"`
	Via      string     `json:"via,omitempty"`
	At       time.Time  `json:"at"`
}

// Expect is the effect a plan promises. All four of metric, direction,
// magnitude and window are required; any one missing makes it unjudgeable.
type Expect struct {
	Metric     string    `json:"metric"`
	Direction  Direction `json:"direction"`
	By         float64   `json:"by"`
	WithinDays int       `json:"within_days"`
	// Scope is where the plan acts, and so where its baseline, guards and
	// acceptance are measured. Defaults to the goal's scope.
	Scope         []semantic.Filter `json:"scope,omitempty"`
	TimeDimension string            `json:"time_dimension,omitempty"`
}

// Action is one call to an external tool that carries the plan out. It runs
// only after a person other than the proposer adopts the plan.
type Action struct {
	Server string         `json:"server"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Why    string         `json:"why,omitempty"`
}

// Plan is stage four: what to do, on the strength of which findings, and what
// it should do to which number.
type Plan struct {
	ID       string    `json:"id"`
	Goal     string    `json:"goal,omitempty"`
	Findings []string  `json:"findings"`
	Does     string    `json:"does"`
	Expect   Expect    `json:"expect"`
	Guards   []Guard   `json:"guards,omitempty"`
	Actions  []Action  `json:"actions,omitempty"`
	Cost     string    `json:"cost,omitempty"`
	By       string    `json:"by"`
	Via      string    `json:"via,omitempty"`
	At       time.Time `json:"at"`
	// Note records choices a reviewer should see, e.g. "no guards declared".
	Note string `json:"note,omitempty"`
}

// Measurement is one metric measured over one scope and window, by the server.
type Measurement struct {
	Metric        string            `json:"metric"`
	Scope         []semantic.Filter `json:"scope,omitempty"`
	TimeDimension string            `json:"time_dimension"`
	From          string            `json:"from"` // inclusive, YYYY-MM-DD
	To            string            `json:"to"`   // exclusive, YYYY-MM-DD
	// Value is nil when there was no data. Never 0 in its place.
	Value *float64 `json:"value"`
	// DataDays is how many days in the window have any rows. Zero means no
	// data, whatever the aggregate came back as.
	DataDays int       `json:"data_days"`
	SQL      string    `json:"sql"`
	ExecMs   int64     `json:"exec_ms"`
	TraceID  string    `json:"trace_id,omitempty"`
	At       time.Time `json:"at"`
}

// Verdict is a decision on a plan.
type Verdict string

const (
	Adopted  Verdict = "adopted"
	Rejected Verdict = "rejected"
)

// Decision is the step before execution: did someone other than the proposer
// agree to it. An adoption pins the baselines measured at that moment.
type Decision struct {
	Plan    string    `json:"plan"`
	Verdict Verdict   `json:"verdict"`
	Who     string    `json:"who"`
	Role    string    `json:"role,omitempty"`
	Why     string    `json:"why,omitempty"`
	At      time.Time `json:"at"`
	// AsOf is the day the windows are anchored on. It equals At's date unless
	// an operator replayed history (consult.allow_as_of), and then it says so.
	AsOf       string `json:"as_of"`
	AsOfForced bool   `json:"as_of_forced,omitempty"`
	// Baseline is the plan metric over the trailing window, pinned here.
	Baseline *Measurement  `json:"baseline,omitempty"`
	Guards   []Measurement `json:"guard_baselines,omitempty"`
}

// ActionRun records one action call: what was sent, what came back.
type ActionRun struct {
	Plan    string         `json:"plan"`
	Index   int            `json:"index"`
	Server  string         `json:"server"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args,omitempty"`
	Result  string         `json:"result,omitempty"`
	Error   string         `json:"error,omitempty"`
	Skipped bool           `json:"skipped,omitempty"`
	Ms      int64          `json:"ms"`
	At      time.Time      `json:"at"`
}

// Outcome is an acceptance verdict.
type Outcome string

const (
	Achieved    Outcome = "achieved"
	Missed      Outcome = "missed"
	GuardBroken Outcome = "guard_broken"
	NoData      Outcome = "no_data"
)

// GuardResult is one guard judged at acceptance (or progress).
type GuardResult struct {
	Guard    Guard        `json:"guard"`
	Baseline *float64     `json:"baseline"`
	Measured *Measurement `json:"measured"`
	Broken   bool         `json:"broken"`
	Why      string       `json:"why,omitempty"`
}

// Acceptance is stage six: the metric measured again over the plan's window,
// judged against the baseline pinned at adoption. Final is false for a
// progress check, which is never a verdict.
type Acceptance struct {
	Plan     string        `json:"plan"`
	Final    bool          `json:"final"`
	Metric   string        `json:"metric"`
	Baseline *float64      `json:"baseline"`
	Target   *float64      `json:"target"`
	Measured *Measurement  `json:"measured"`
	Guards   []GuardResult `json:"guards,omitempty"`
	Outcome  Outcome       `json:"outcome"`
	Says     string        `json:"says"`
	By       string        `json:"by,omitempty"` // who triggered it; "system" for the ticker
	At       time.Time     `json:"at"`
	// Partial is set on a progress check whose window has not closed yet.
	Partial bool `json:"partial,omitempty"`
}

// Rule names which consulting rule refused a request.
type Rule string

const (
	RuleUnknownMetric    Rule = "unknown_metric"
	RuleVagueGoal        Rule = "vague_goal"
	RuleBadGuard         Rule = "bad_guard"
	RuleNoEvidence       Rule = "finding_without_evidence"
	RuleEvidenceFailed   Rule = "evidence_query_failed"
	RuleNoFinding        Rule = "plan_without_finding"
	RuleUnknownFinding   Rule = "unknown_finding"
	RuleVagueExpectation Rule = "vague_expectation"
	RuleNoTimeDimension  Rule = "no_time_dimension"
	RuleBadScope         Rule = "bad_scope"
	RuleBadAction        Rule = "bad_action"
	RuleSelfApproval     Rule = "self_approval"
	RuleNotAllowed       Rule = "role_not_allowed"
	RuleAnonymous        Rule = "anonymous"
	RuleAlreadyDecided   Rule = "already_decided"
	RuleNoBaseline       Rule = "no_baseline"
	RuleNotAdopted       Rule = "not_adopted"
	RuleTooEarly         Rule = "too_early"
	RuleAlreadyAccepted  Rule = "already_accepted"
	RuleAsOfNotAllowed   Rule = "as_of_not_allowed"
	RuleWhyRequired      Rule = "why_required"
	RuleNotFound         Rule = "not_found"
)

// Refusal is a request the loop's rules turned down. It is not a failure of
// the system; it is the system working.
type Refusal struct {
	Rule Rule
	Msg  string
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(rule Rule, format string, a ...any) error {
	return &Refusal{Rule: rule, Msg: fmt.Sprintf(format, a...)}
}

// RuleOf returns the rule behind err, or "" when err is not a refusal.
func RuleOf(err error) Rule {
	for err != nil {
		if r, ok := err.(*Refusal); ok {
			return r.Rule
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = u.Unwrap()
	}
	return ""
}

// samePerson compares users, not user/role pairs: proposing as a consultant
// and approving as an admin is exactly what the two-person rule stops.
func samePerson(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func target(baseline float64, d Direction, by float64) float64 {
	if d == Down {
		return baseline * (1 - by)
	}
	return baseline * (1 + by)
}
