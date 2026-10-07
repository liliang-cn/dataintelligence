package consult

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

// The fixture is a tiny, domain-free warehouse in SQLite: two lines, daily
// runs. Before 2026-03-01 line A makes 100/day and line B 50/day; from
// 2026-03-01 line A makes 120/day and line B stops reporting. Scrap is 2/day
// on A throughout, unless a test raises it.

const fixtureModel = `
entities:
  - {name: run,  table: runs,  primary_key: run_id}
  - {name: line, table: lines, primary_key: line_id}
  - {name: site, table: sites, primary_key: site_id}
joins:
  - {from: run, to: line, cardinality: many_to_one, from_key: line_id, to_key: line_id}
dimensions:
  - {name: line_name, entity: line, column: name, type: categorical}
  - {name: run_date,  entity: run,  column: day,  type: time}
  - {name: site_name, entity: site, column: name, type: categorical}
metrics:
  - {name: output, description: units made, entity: run, agg: sum, expr: qty}
  - {name: scrap,  description: units scrapped, entity: run, agg: sum, expr: scrap}
  - {name: site_count, description: sites, entity: site, agg: count, expr: site_id}
  - {name: good_rate, description: 良率, formula: "1.0 * (output - scrap) / nullif(output, 0)"}
`

var adoptDay = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

type fixture struct {
	svc   *Service
	clock *time.Time
	runs  *fakeRunner
}

func newFixture(t *testing.T, afterScrapA float64) *fixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wh.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE lines (line_id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE sites (site_id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE runs (run_id INTEGER PRIMARY KEY, line_id INTEGER, day TEXT, qty REAL, scrap REAL)`,
		`INSERT INTO lines VALUES (1, 'A'), (2, 'B'), (3, 'C')`,
		`INSERT INTO sites VALUES (1, 'S')`,
	}
	id := 0
	for d := adoptDay.AddDate(0, 0, -60); d.Before(adoptDay.AddDate(0, 0, 60)); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		after := !d.Before(Day(adoptDay))
		qa, sa := 100.0, 2.0
		if after {
			qa, sa = 120, afterScrapA
		}
		id++
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO runs VALUES (%d, 1, '%s', %g, %g)`, id, day, qa, sa))
		if !after {
			id++
			stmts = append(stmts, fmt.Sprintf(`INSERT INTO runs VALUES (%d, 2, '%s', 50, 1)`, id, day))
		}
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	_ = raw.Close()
	modelPath := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(modelPath, []byte(fixtureModel), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(context.Background(), modelPath, "sqlite://"+dbPath+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	st, err := OpenStore(filepath.Join(dir, "consult.db"), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := adoptDay
	runs := &fakeRunner{actions: map[string]bool{"plant/apply": true}}
	f := &fixture{clock: &clock, runs: runs}
	f.svc = &Service{
		Store: st, Model: eng.Model, Dialect: eng.Dialect,
		Query:     Governed(eng, governance.Policy{}),
		MeasureAs: governance.Principal{User: "consult", Role: "analyst"},
		Actions:   runs,
		Now:       func() time.Time { return *f.clock },
	}
	return f
}

type fakeRunner struct {
	actions map[string]bool
	calls   []string
	fail    bool
}

func (r *fakeRunner) ValidateAction(server, tool string) error {
	if !r.actions[server+"/"+tool] {
		return fmt.Errorf("%s/%s is not a configured action", server, tool)
	}
	return nil
}

func (r *fakeRunner) CallAction(_ context.Context, server, tool string, args map[string]any) (string, error) {
	r.calls = append(r.calls, fmt.Sprintf("%s/%s %v", server, tool, args))
	if r.fail {
		return "", fmt.Errorf("boom")
	}
	return `{"ok":true}`, nil
}

var (
	alice = governance.Principal{User: "alice", Role: "analyst"}
	bob   = governance.Principal{User: "bob", Role: "manager"}
	ctx   = context.Background()
	lineA = []semantic.Filter{{Dimension: "line_name", Op: "=", Values: []any{"A"}}}
)

func ptr(f float64) *float64 { return &f }

func wantRule(t *testing.T, err error, r Rule) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected refusal %s, got nil", r)
	}
	if got := RuleOf(err); got != r {
		t.Fatalf("expected refusal %s, got %q (%v)", r, got, err)
	}
}

// seed records a goal and a finding on line A and returns their ids.
func (f *fixture) seed(t *testing.T) (string, string) {
	t.Helper()
	g, err := f.svc.AddGoal(ctx, GoalInput{What: "A 线产量提一成", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30, Scope: lineA}, alice, "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := f.svc.AddFinding(ctx, FindingInput{Goal: g.ID, Says: "A 线产量是 B 线的两倍", Evidence: []EvidenceInput{
		{Asked: "按线的产量", Query: semantic.Query{Metrics: []string{"output"}, GroupBy: []string{"line_name"}}},
	}}, alice, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	return g.ID, fd.ID
}

func (f *fixture) plan(t *testing.T, goal, finding string, mod func(*PlanInput)) *Plan {
	t.Helper()
	in := PlanInput{Goal: goal, Findings: []string{finding}, Does: "调整 A 线节拍",
		Expect:  Expect{Metric: "output", Direction: Up, By: 0.1, WithinDays: 30},
		Guards:  []Guard{{Metric: "scrap", WorseIs: Up, Tolerance: ptr(0.1)}},
		Actions: []Action{{Server: "plant", Tool: "apply", Args: map[string]any{"line": "A"}}}}
	if mod != nil {
		mod(&in)
	}
	p, err := f.svc.ProposePlan(ctx, in, alice, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// --- goal ---

func TestGoalMetricMustExistInModel(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "revenue", Direction: Up, By: 0.1, WithinDays: 30}, alice, "")
	wantRule(t, err, RuleUnknownMetric)
	_, err = f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30,
		Guards: []Guard{{Metric: "nope", WorseIs: Up, Limit: ptr(1)}}}, alice, "")
	wantRule(t, err, RuleUnknownMetric)
}

func TestGoalNeedsDirectionMagnitudeAndDeadline(t *testing.T) {
	f := newFixture(t, 2)
	for _, in := range []GoalInput{
		{What: "x", Metric: "output", By: 0.1, WithinDays: 30},
		{What: "x", Metric: "output", Direction: Up, WithinDays: 30},
		{What: "x", Metric: "output", Direction: Up, By: 0.1},
	} {
		_, err := f.svc.AddGoal(ctx, in, alice, "")
		wantRule(t, err, RuleVagueGoal)
	}
}

// A rate cannot be pushed past 100%: line A's good rate is 98%, so +5% (relative) is refused
// and +1% is not.
func TestRateGoalMustStayWithinAHundredPercent(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "good_rate", Direction: Up, By: 0.05, WithinDays: 30, Scope: lineA}, alice, "")
	wantRule(t, err, RuleOutOfRange)
	if g, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "good_rate", Direction: Up, By: 0.01, WithinDays: 30, Scope: lineA}, alice, ""); err != nil || *g.Target() > 1 {
		t.Fatalf("goal within range: %v %v", g, err)
	}
	// a count is not a rate: output may grow past any bound
	if _, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "output", Direction: Up, By: 2, WithinDays: 30, Scope: lineA}, alice, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGoalWithoutATimeDimensionIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "site_count", Direction: Up, By: 0.1, WithinDays: 30}, alice, "")
	wantRule(t, err, RuleNoTimeDimension)
}

// The goal's baseline is measured by the server over the trailing window, in
// scope: line A only (100/day × 30), not the whole business (150/day × 30).
func TestGoalBaselineIsMeasuredInScopeAndWindow(t *testing.T) {
	f := newFixture(t, 2)
	g, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30, Scope: lineA}, alice, "")
	if err != nil {
		t.Fatal(err)
	}
	b := g.Baseline
	if b == nil || b.Value == nil || *b.Value != 3000 {
		t.Fatalf("baseline = %+v, want 3000", b)
	}
	if b.DataDays != 30 {
		t.Fatalf("data days = %d, want 30", b.DataDays)
	}
	if b.From != "2026-01-30" || b.To != "2026-03-01" || b.TimeDimension != "run_date" {
		t.Fatalf("window = %s ~ %s on %s", b.From, b.To, b.TimeDimension)
	}
	if !strings.Contains(b.SQL, ">=") || !strings.Contains(b.SQL, "<") {
		t.Fatalf("window must be in the compiled SQL: %s", b.SQL)
	}
	whole, err := f.svc.AddGoal(ctx, GoalInput{What: "y", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30}, alice, "")
	if err != nil {
		t.Fatal(err)
	}
	if *whole.Baseline.Value != 4500 {
		t.Fatalf("unscoped baseline = %v, want 4500", *whole.Baseline.Value)
	}
	if tg := g.Target(); tg == nil || *tg < 3299.99 || *tg > 3300.01 {
		t.Fatalf("target = %v", tg)
	}
}

func TestScopeOnAnUnknownDimensionIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30,
		Scope: []semantic.Filter{{Dimension: "plant", Op: "=", Values: []any{"1"}}}}, alice, "")
	wantRule(t, err, RuleBadScope)
	// Known but unreachable from the metric: the compiler says no, up front.
	_, err = f.svc.AddGoal(ctx, GoalInput{What: "x", Metric: "output", Direction: Up, By: 0.1, WithinDays: 30,
		Scope: []semantic.Filter{{Dimension: "site_name", Op: "=", Values: []any{"S"}}}}, alice, "")
	wantRule(t, err, RuleBadScope)
}

// --- finding ---

func TestFindingWithoutEvidenceIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddFinding(ctx, FindingInput{Says: "A 线很好"}, alice, "")
	wantRule(t, err, RuleNoEvidence)
}

func TestFindingEvidenceThatFailsToRunIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	_, err := f.svc.AddFinding(ctx, FindingInput{Says: "x", Evidence: []EvidenceInput{
		{Asked: "?", Query: semantic.Query{Metrics: []string{"made_up"}}},
	}}, alice, "")
	wantRule(t, err, RuleEvidenceFailed)
}

// Evidence is what the server ran and got: SQL, rows, count — not a claim.
func TestFindingStoresWhatTheServerExecuted(t *testing.T) {
	f := newFixture(t, 2)
	_, fid := f.seed(t)
	b, err := f.svc.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fd := b.Goals[0].Findings[0]
	if fd.ID != fid || len(fd.Evidence) != 1 {
		t.Fatalf("finding = %+v", fd)
	}
	e := fd.Evidence[0]
	if e.SQL == "" || e.RowCount != 2 || len(e.Rows) != 2 || e.At.IsZero() {
		t.Fatalf("evidence = %+v", e)
	}
	if e.Value != nil {
		t.Fatalf("a two-row answer has no scalar, got %v", *e.Value)
	}
}

// --- plan ---

func TestPlanWithoutFindingIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	g, _ := f.seed(t)
	_, err := f.svc.ProposePlan(ctx, PlanInput{Goal: g, Does: "x", Expect: Expect{Metric: "output", Direction: Up, By: 0.1, WithinDays: 30}}, alice, "")
	wantRule(t, err, RuleNoFinding)
	_, err = f.svc.ProposePlan(ctx, PlanInput{Goal: g, Findings: []string{"f99"}, Does: "x", Expect: Expect{Metric: "output", Direction: Up, By: 0.1, WithinDays: 30}}, alice, "")
	wantRule(t, err, RuleUnknownFinding)
}

func TestPlanExpectationNeedsAllFour(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	for _, e := range []Expect{
		{Direction: Up, By: 0.1, WithinDays: 30},
		{Metric: "output", By: 0.1, WithinDays: 30},
		{Metric: "output", Direction: Up, WithinDays: 30},
		{Metric: "output", Direction: Up, By: 0.1},
	} {
		_, err := f.svc.ProposePlan(ctx, PlanInput{Goal: g, Findings: []string{fid}, Does: "x", Expect: e}, alice, "")
		wantRule(t, err, RuleVagueExpectation)
	}
}

func TestPlanGuardNeedsAThreshold(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	_, err := f.svc.ProposePlan(ctx, PlanInput{Goal: g, Findings: []string{fid}, Does: "x",
		Expect: Expect{Metric: "output", Direction: Up, By: 0.1, WithinDays: 30},
		Guards: []Guard{{Metric: "scrap", WorseIs: Up}}}, alice, "")
	wantRule(t, err, RuleBadGuard)
}

func TestPlanActionMustBeAConfiguredAction(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	_, err := f.svc.ProposePlan(ctx, PlanInput{Goal: g, Findings: []string{fid}, Does: "x",
		Expect:  Expect{Metric: "output", Direction: Up, By: 0.1, WithinDays: 30},
		Actions: []Action{{Server: "plant", Tool: "read_tags"}}}, alice, "")
	wantRule(t, err, RuleBadAction)
}

func TestPlanInheritsTheGoalScope(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	if len(p.Expect.Scope) != 1 || p.Expect.Scope[0].Dimension != "line_name" {
		t.Fatalf("scope = %+v", p.Expect.Scope)
	}
}

// --- adoption ---

func TestTheProposerCannotAdopt(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	_, err := f.svc.Adopt(ctx, p.ID, governance.Principal{User: " ALICE ", Role: "admin"}, "", "")
	wantRule(t, err, RuleSelfApproval)
	_, err = f.svc.Adopt(ctx, p.ID, governance.Principal{User: "anon"}, "", "")
	wantRule(t, err, RuleAnonymous)
	f.svc.AdoptRoles = []string{"admin"}
	_, err = f.svc.Adopt(ctx, p.ID, bob, "", "")
	wantRule(t, err, RuleNotAllowed)
	if len(f.runs.calls) != 0 {
		t.Fatalf("no action may run on a refused adoption: %v", f.runs.calls)
	}
}

// Adoption measures the baseline at that moment, pins it with the guards',
// and only then runs the actions — recording each call.
func TestAdoptionPinsTheBaselineThenRunsActions(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	res, err := f.svc.Adopt(ctx, p.ID, bob, "试一个月", "")
	if err != nil {
		t.Fatal(err)
	}
	d := res.Decision
	if d.Baseline == nil || *d.Baseline.Value != 3000 || d.AsOf != "2026-03-01" {
		t.Fatalf("baseline = %+v as_of %s", d.Baseline, d.AsOf)
	}
	if len(d.Guards) != 1 || *d.Guards[0].Value != 60 {
		t.Fatalf("guard baselines = %+v", d.Guards)
	}
	if len(f.runs.calls) != 1 || len(res.Actions) != 1 || res.Actions[0].Result == "" {
		t.Fatalf("actions = %+v / %v", res.Actions, f.runs.calls)
	}
	runs, _ := f.svc.Store.ActionRuns(ctx, p.ID)
	if len(runs) != 1 || runs[0].Args["line"] != "A" {
		t.Fatalf("stored runs = %+v", runs)
	}
	_, err = f.svc.Adopt(ctx, p.ID, governance.Principal{User: "carol"}, "", "")
	wantRule(t, err, RuleAlreadyDecided)
	if len(f.runs.calls) != 1 {
		t.Fatal("a refused second adoption must not run actions again")
	}
}

func TestAFailedActionStopsTheRestAndIsRecorded(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, func(in *PlanInput) {
		in.Actions = append(in.Actions, Action{Server: "plant", Tool: "apply", Args: map[string]any{"step": 2}})
	})
	f.runs.fail = true
	res, err := f.svc.Adopt(ctx, p.ID, bob, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Actions[0].Error == "" || !res.Actions[1].Skipped || len(f.runs.calls) != 1 {
		t.Fatalf("actions = %+v", res.Actions)
	}
}

// No data is nil, never 0 — and a plan with no baseline cannot be adopted,
// because it could never be accepted.
func TestNoBaselineRefusesAdoption(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, func(in *PlanInput) {
		in.Expect.Scope = []semantic.Filter{{Dimension: "line_name", Op: "=", Values: []any{"C"}}}
	})
	_, err := f.svc.Adopt(ctx, p.ID, bob, "", "")
	wantRule(t, err, RuleNoBaseline)
	if len(f.runs.calls) != 0 {
		t.Fatal("actions ran without a baseline")
	}
}

// --- acceptance ---

func TestAcceptanceRefusesBeforeTheWindowCloses(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	_, err := f.svc.Accept(ctx, p.ID, bob, "")
	wantRule(t, err, RuleNotAdopted)
	if _, err := f.svc.Adopt(ctx, p.ID, bob, "", ""); err != nil {
		t.Fatal(err)
	}
	*f.clock = adoptDay.AddDate(0, 0, 10)
	_, err = f.svc.Accept(ctx, p.ID, bob, "")
	wantRule(t, err, RuleTooEarly)
}

// Acceptance re-measures the window after adoption, in scope: line A made
// 120/day → 3600 against a 3000 baseline and a 3300 line.
func TestAcceptanceReMeasuresInScopeAndWindow(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	if _, err := f.svc.Adopt(ctx, p.ID, bob, "", ""); err != nil {
		t.Fatal(err)
	}
	*f.clock = adoptDay.AddDate(0, 0, 31)
	due, err := f.svc.DuePlans(ctx)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %v %v", due, err)
	}
	a, err := f.svc.Accept(ctx, p.ID, governance.Principal{User: "system"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != Achieved || *a.Measured.Value != 3600 || a.Measured.From != "2026-03-01" || a.Measured.To != "2026-03-31" {
		t.Fatalf("acceptance = %+v / %+v", a, a.Measured)
	}
	if !a.Final || a.Guards[0].Broken {
		t.Fatalf("guards = %+v", a.Guards)
	}
	_, err = f.svc.Accept(ctx, p.ID, bob, "")
	wantRule(t, err, RuleAlreadyAccepted)
}

// The main metric hit, but scrap doubled: not a success.
func TestABrokenGuardIsNotASuccessEvenIfTheMetricHit(t *testing.T) {
	f := newFixture(t, 4)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	if _, err := f.svc.Adopt(ctx, p.ID, bob, "", ""); err != nil {
		t.Fatal(err)
	}
	*f.clock = adoptDay.AddDate(0, 0, 30)
	a, err := f.svc.Accept(ctx, p.ID, bob, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != GuardBroken || !a.Guards[0].Broken {
		t.Fatalf("outcome = %s: %s", a.Outcome, a.Says)
	}
	if !strings.Contains(a.Says, "主指标达标了") {
		t.Fatalf("says = %s", a.Says)
	}
}

// Line B stops reporting after adoption: the acceptance has no data, and it
// says so instead of calling it a drop to zero.
func TestNoDataAtAcceptanceIsNoDataNotZero(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, func(in *PlanInput) {
		in.Expect.Scope = []semantic.Filter{{Dimension: "line_name", Op: "=", Values: []any{"B"}}}
		in.Guards = nil
	})
	if _, err := f.svc.Adopt(ctx, p.ID, bob, "", ""); err != nil {
		t.Fatal(err)
	}
	*f.clock = adoptDay.AddDate(0, 0, 30)
	a, err := f.svc.Accept(ctx, p.ID, bob, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != NoData || a.Measured.Value != nil {
		t.Fatalf("outcome = %s measured %+v", a.Outcome, a.Measured)
	}
}

func TestProgressIsNotAVerdict(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	if _, err := f.svc.Adopt(ctx, p.ID, bob, "", ""); err != nil {
		t.Fatal(err)
	}
	*f.clock = adoptDay.AddDate(0, 0, 9)
	a, err := f.svc.Measure(ctx, p.ID, bob)
	if err != nil {
		t.Fatal(err)
	}
	if a.Final || !a.Partial || a.Measured.To != "2026-03-11" || *a.Measured.Value != 1200 {
		t.Fatalf("progress = %+v %+v", a, a.Measured)
	}
	if acc, _ := f.svc.Store.Acceptance(ctx, p.ID); acc != nil {
		t.Fatal("progress must not be stored as an acceptance")
	}
	v, _ := f.svc.PlanView(ctx, p.ID)
	if v.Progress == nil || v.State != "adopted" {
		t.Fatalf("view = %+v", v)
	}
}

// Replaying history: with allow_as_of an operator anchors on a past day; the
// decision records that it was forced, and the window is relative to it.
func TestAsOfIsRefusedUnlessAllowedAndRecordedWhenUsed(t *testing.T) {
	f := newFixture(t, 2)
	g, fid := f.seed(t)
	p := f.plan(t, g, fid, nil)
	*f.clock = adoptDay.AddDate(0, 0, 90)
	_, err := f.svc.Adopt(ctx, p.ID, bob, "", "2026-03-01")
	wantRule(t, err, RuleAsOfNotAllowed)
	f.svc.AllowAsOf = true
	res, err := f.svc.Adopt(ctx, p.ID, bob, "", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Decision.AsOfForced || *res.Decision.Baseline.Value != 3000 {
		t.Fatalf("decision = %+v", res.Decision)
	}
	acc, errs := f.svc.AcceptDue(ctx)
	if len(errs) != 0 || len(acc) != 1 || acc[0].Outcome != Achieved {
		t.Fatalf("accept due = %+v %v", acc, errs)
	}
}

// --- judge (pure) ---

func TestJudgeEdges(t *testing.T) {
	p := &Plan{ID: "p", Expect: Expect{Metric: "m", Direction: Up, By: 0.5, WithinDays: 30}}
	m := func(v float64) *Measurement { return &Measurement{Value: &v} }
	at := time.Now()
	if a := Judge(p, ptr(100), m(150), nil, true, at); a.Outcome != Achieved {
		t.Fatalf("exactly on target must count: %s", a.Says)
	}
	if a := Judge(p, ptr(100), m(149.999), nil, true, at); a.Outcome != Missed {
		t.Fatalf("just under must not round up: %s", a.Says)
	}
	if a := Judge(p, ptr(100), m(90), nil, true, at); a.Outcome != Missed || !strings.Contains(a.Says, "反了") {
		t.Fatalf("wrong way: %s", a.Says)
	}
	if a := Judge(p, nil, m(90), nil, true, at); a.Outcome != NoData {
		t.Fatalf("no baseline: %s", a.Says)
	}
	if a := Judge(p, ptr(100), &Measurement{}, nil, true, at); a.Outcome != NoData {
		t.Fatalf("nil measured: %s", a.Says)
	}
	down := &Plan{ID: "p", Expect: Expect{Metric: "m", Direction: Down, By: 0.2, WithinDays: 30}}
	if a := Judge(down, ptr(100), m(80), nil, true, at); a.Outcome != Achieved {
		t.Fatalf("down: %s", a.Says)
	}
}

func TestGuardJudging(t *testing.T) {
	tol := Guard{Metric: "g", WorseIs: Up, Tolerance: ptr(0.1)}
	if b, _ := guardBroken(tol, ptr(100), ptr(110)); b {
		t.Fatal("exactly at tolerance holds")
	}
	if b, _ := guardBroken(tol, ptr(100), ptr(110.5)); !b {
		t.Fatal("beyond tolerance breaks")
	}
	if b, _ := guardBroken(tol, ptr(100), nil); !b {
		t.Fatal("unmeasurable guard counts as broken")
	}
	lim := Guard{Metric: "g", WorseIs: Down, Limit: ptr(0.2)}
	if b, _ := guardBroken(lim, nil, ptr(0.19)); !b {
		t.Fatal("below a down-limit breaks")
	}
}

func TestHumanNumbers(t *testing.T) {
	for in, want := range map[float64]string{1234: "1,234", 1234.5: "1,234.50", -1234567.891: "-1,234,567.89", 0: "0", 999: "999"} {
		if got := human(in); got != want {
			t.Errorf("human(%v) = %q, want %q", in, got, want)
		}
	}
}
