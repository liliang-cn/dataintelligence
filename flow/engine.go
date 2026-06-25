// Package flow is the platform's Run plane: a workflow engine that runs steps in
// order, pauses at Human nodes for approval, and supports saga-style rollback
// (each step's Compensate is run in reverse on rollback/reject). Every run is
// journaled to Postgres so the execution dashboard, history, and chain-of-change
// survive restarts and are auditable.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Run statuses.
const (
	StatusRunning    = "running"
	StatusAwaiting   = "awaiting_approval"
	StatusCompleted  = "completed"
	StatusRejected   = "rejected"
	StatusRolledBack = "rolled_back"
	StatusFailed     = "failed"
)

// Step is one node in a flow. Human steps pause the run for approval and carry no
// Do/Undo. A mutating step provides Undo so the run can be rolled back.
type Step struct {
	Name  string
	Human bool
	Do    func(ctx context.Context, rc *RunContext) error
	Undo  func(ctx context.Context, rc *RunContext) error
}

// RunContext is passed to each step: the run id, shared (persisted) state, and
// the warehouse handle.
type RunContext struct {
	RunID string
	State map[string]any
	WH    *warehouse.Warehouse
}

// StepRecord is one journal entry.
type StepRecord struct {
	Name   string `json:"name"`
	Status string `json:"status"` // done | compensated | failed | approved | rejected
	Info   string `json:"info,omitempty"`
	At     string `json:"at"`
}

// Run is a flow execution (the dashboard row + journal + chain-of-change).
type Run struct {
	ID     string         `json:"id"`
	Flow   string         `json:"flow"`
	Status string         `json:"status"`
	Cursor int            `json:"cursor"` // index of the next step to run
	State  map[string]any `json:"state"`
	Steps  []StepRecord   `json:"steps"`
}

// Engine runs flows and persists their runs.
type Engine struct {
	wh    *warehouse.Warehouse
	flows map[string][]Step
	now   func() time.Time
	newID func() string
}

func NewEngine(wh *warehouse.Warehouse) *Engine {
	return &Engine{
		wh:    wh,
		flows: map[string][]Step{},
		now:   time.Now,
		newID: func() string { return fmt.Sprintf("run-%d", time.Now().UnixNano()) },
	}
}

func (e *Engine) Register(name string, steps []Step) { e.flows[name] = steps }

// Names lists registered flow names.
func (e *Engine) Names() []string {
	out := make([]string, 0, len(e.flows))
	for n := range e.flows {
		out = append(out, n)
	}
	return out
}

func (e *Engine) ensureTable(ctx context.Context) error {
	_, err := e.wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _flow_runs (
		id text PRIMARY KEY, flow text, status text, doc jsonb,
		updated_at timestamptz DEFAULT now())`)
	return err
}

// Start creates a run and advances it until completion or a Human pause.
func (e *Engine) Start(ctx context.Context, flowName string, initState map[string]any) (*Run, error) {
	steps, ok := e.flows[flowName]
	if !ok {
		return nil, fmt.Errorf("unknown flow %q", flowName)
	}
	if err := e.ensureTable(ctx); err != nil {
		return nil, err
	}
	if initState == nil {
		initState = map[string]any{}
	}
	run := &Run{ID: e.newID(), Flow: flowName, Status: StatusRunning, State: initState}
	if err := e.save(ctx, run); err != nil {
		return nil, err
	}
	return e.advance(ctx, run, steps)
}

// Approve resumes a run paused at a Human step.
func (e *Engine) Approve(ctx context.Context, id string) (*Run, error) {
	run, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	steps := e.flows[run.Flow]
	if run.Status != StatusAwaiting {
		return nil, fmt.Errorf("run %s is %q, not awaiting approval", id, run.Status)
	}
	e.record(run, steps[run.Cursor].Name, "approved", "")
	run.Cursor++
	run.Status = StatusRunning
	if err := e.save(ctx, run); err != nil {
		return nil, err
	}
	return e.advance(ctx, run, steps)
}

// Reject rejects a paused run and rolls back everything done so far.
func (e *Engine) Reject(ctx context.Context, id string) (*Run, error) {
	run, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusAwaiting {
		return nil, fmt.Errorf("run %s is %q, not awaiting approval", id, run.Status)
	}
	e.record(run, e.flows[run.Flow][run.Cursor].Name, "rejected", "")
	if err := e.compensate(ctx, run); err != nil {
		return run, err
	}
	run.Status = StatusRejected
	return run, e.save(ctx, run)
}

// Rollback compensates a completed (or failed) run.
func (e *Engine) Rollback(ctx context.Context, id string) (*Run, error) {
	run, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := e.compensate(ctx, run); err != nil {
		return run, err
	}
	run.Status = StatusRolledBack
	return run, e.save(ctx, run)
}

// advance runs steps from the cursor until a Human pause, completion, or failure.
func (e *Engine) advance(ctx context.Context, run *Run, steps []Step) (*Run, error) {
	for run.Cursor < len(steps) {
		step := steps[run.Cursor]
		if step.Human {
			run.Status = StatusAwaiting
			return run, e.save(ctx, run)
		}
		rc := &RunContext{RunID: run.ID, State: run.State, WH: e.wh}
		if err := step.Do(ctx, rc); err != nil {
			e.record(run, step.Name, "failed", err.Error())
			_ = e.compensate(ctx, run)
			run.Status = StatusFailed
			_ = e.save(ctx, run)
			return run, fmt.Errorf("step %q failed: %w", step.Name, err)
		}
		run.State = rc.State
		e.record(run, step.Name, "done", "")
		run.Cursor++
		if err := e.save(ctx, run); err != nil {
			return run, err
		}
	}
	run.Status = StatusCompleted
	return run, e.save(ctx, run)
}

// compensate runs Undo for every completed step in reverse order.
func (e *Engine) compensate(ctx context.Context, run *Run) error {
	steps := e.flows[run.Flow]
	for i := run.Cursor - 1; i >= 0; i-- {
		st := steps[i]
		if st.Undo == nil {
			continue
		}
		rc := &RunContext{RunID: run.ID, State: run.State, WH: e.wh}
		if err := st.Undo(ctx, rc); err != nil {
			e.record(run, st.Name, "failed", "compensate: "+err.Error())
			return err
		}
		e.record(run, st.Name, "compensated", "")
	}
	return nil
}

func (e *Engine) record(run *Run, name, status, info string) {
	run.Steps = append(run.Steps, StepRecord{Name: name, Status: status, Info: info, At: e.now().UTC().Format(time.RFC3339)})
}

func (e *Engine) save(ctx context.Context, run *Run) error {
	doc, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = e.wh.Exec(ctx, `INSERT INTO _flow_runs (id, flow, status, doc, updated_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (id) DO UPDATE SET status=$3, doc=$4, updated_at=now()`,
		run.ID, run.Flow, run.Status, string(doc))
	return err
}

func (e *Engine) Get(ctx context.Context, id string) (*Run, error) {
	res, err := e.wh.Query(ctx, `SELECT doc FROM _flow_runs WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("run %q not found", id)
	}
	return decodeRun(res.Rows[0][0])
}

func (e *Engine) List(ctx context.Context) ([]*Run, error) {
	res, err := e.wh.Query(ctx, `SELECT doc FROM _flow_runs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, row := range res.Rows {
		r, err := decodeRun(row[0])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func decodeRun(v any) (*Run, error) {
	var b []byte
	switch t := v.(type) {
	case []byte:
		b = t
	case string:
		b = []byte(t)
	default:
		return nil, fmt.Errorf("unexpected doc type %T", v)
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
