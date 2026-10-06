package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/agenttools"
	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

type tool struct {
	name, desc string
	schema     map[string]any
	readOnly   bool
	handler    func(context.Context, map[string]any) (any, error)
}

// maxRowsToModel bounds what one query hands back to the model. The row count
// is always reported, so a truncated answer is never mistaken for a full one.
const maxRowsToModel = 200

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var filterSchema = obj(map[string]any{
	"dimension": str("dimension name"),
	"op":        map[string]any{"type": "string", "enum": semantic.FilterOperators()},
	"values":    map[string]any{"type": "array", "items": map[string]any{}, "description": "one value for =,!=,>,<…; many for in; two for between; none for is null"},
}, "dimension", "op")

var filtersSchema = map[string]any{"type": "array", "items": filterSchema, "description": "dimension filters, AND-ed"}

var timeSchema = obj(map[string]any{
	"dimension": str("time dimension (optional when the metric has exactly one)"),
	"from":      str("inclusive start date YYYY-MM-DD"),
	"to":        str("exclusive end date YYYY-MM-DD"),
	"last_days": map[string]any{"type": "integer", "description": "alternative to from/to: the last N days up to and including today"},
})

// querySchema is the semantic query shape shared by query_metric and evidence.
var querySchema = obj(map[string]any{
	"metrics":    strs("metric names (from list_metrics)"),
	"group_by":   strs("dimension names (from get_dimensions)"),
	"filters":    filtersSchema,
	"time":       timeSchema,
	"time_grain": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "quarter", "year"}},
	"order_by":   str("a metric or dimension to sort by"),
	"desc":       map[string]any{"type": "boolean"},
	"limit":      map[string]any{"type": "integer"},
}, "metrics")

var guardsSchema = map[string]any{"type": "array", "description": "metrics that must not get worse", "items": obj(map[string]any{
	"metric":    str("guard metric"),
	"worse_is":  map[string]any{"type": "string", "enum": []string{"up", "down"}, "description": "which way is worse"},
	"tolerance": num("relative worsening allowed vs its baseline, e.g. 0.05"),
	"limit":     num("absolute bound it may not cross"),
}, "metric", "worse_is")}

func buildTools(ctx context.Context, eng *engine.Engine, pol governance.Policy, opts Options) []tool {
	who := func(ctx context.Context) governance.Principal {
		if p, ok := ctx.Value(principalKey{}).(governance.Principal); ok && p.User != "" {
			return p
		}
		return opts.Principal
	}
	ts := []tool{
		{name: "describe_warehouse", desc: "List the warehouse tables and their column counts.", readOnly: true,
			schema: obj(map[string]any{}),
			handler: func(ctx context.Context, _ map[string]any) (any, error) {
				return agenttools.DescribeWarehouse(ctx, eng)
			}},
		{name: "list_metrics", desc: "List the semantic metrics with descriptions.", readOnly: true,
			schema: obj(map[string]any{}),
			handler: func(context.Context, map[string]any) (any, error) {
				return agenttools.ListMetrics(eng), nil
			}},
		{name: "get_dimensions", desc: "Dimensions a metric can be grouped or filtered by without a fan-out, with their types (time dimensions can carry windows). Call before query_metric.", readOnly: true,
			schema: obj(map[string]any{"metric": str("metric name")}, "metric"),
			handler: func(_ context.Context, a map[string]any) (any, error) {
				name := fmt.Sprint(a["metric"])
				dims, err := agenttools.Dimensions(eng, name)
				if err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				out := make([]map[string]string, 0, len(dims))
				for _, d := range dims {
					typ := "categorical"
					if dd := eng.Model.Dimension(d); dd != nil && dd.Type != "" {
						typ = dd.Type
					}
					out = append(out, map[string]string{"name": d, "type": typ})
				}
				return map[string]any{"metric": name, "dimensions": out}, nil
			}},
		{name: "query_metric", readOnly: true,
			desc:   "Run a governed semantic query: metrics × group_by, with dimension filters, a time window on a time dimension, a time grain, ordering and a limit. Returns the compiled SQL with the rows. Governance (RBAC, row filters, masking) applies to your identity.",
			schema: querySchema,
			handler: func(ctx context.Context, a map[string]any) (any, error) {
				q, err := agenttools.ParseQuery(eng.Model, a, time.Now())
				if err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				p := who(ctx)
				ans, err := agenttools.Query(ctx, eng, pol, p, q)
				if err != nil {
					return map[string]any{"error": "refused by governance: " + err.Error()}, nil
				}
				rows := ans.Rows
				out := map[string]any{"columns": ans.Columns, "row_count": len(rows), "sql": ans.SQL}
				if len(rows) > maxRowsToModel {
					rows = rows[:maxRowsToModel]
					out["truncated_to"] = maxRowsToModel
				}
				out["rows"] = rows
				return out, nil
			}},
	}
	if opts.ChecksPath != "" {
		ts = append(ts, tool{name: "health_check", readOnly: true,
			desc:   "Detect cross-source data conflicts declared in the deployment's checks.",
			schema: obj(map[string]any{}),
			handler: func(ctx context.Context, _ map[string]any) (any, error) {
				return agenttools.HealthCheck(ctx, eng, opts.ChecksPath)
			}})
	}
	if opts.Remote != nil {
		for _, spec := range opts.Remote.ReadTools(ctx) {
			spec := spec
			ts = append(ts, tool{name: spec.Name, desc: spec.Description + " (external system; read-only)", schema: spec.Schema, readOnly: true,
				handler: func(ctx context.Context, a map[string]any) (any, error) {
					out, err := opts.Remote.Call(ctx, spec.Server, spec.Tool, a)
					if err != nil {
						return map[string]any{"error": err.Error(), "result": out}, nil
					}
					return out, nil
				}})
		}
	}
	if opts.Consult != nil {
		ts = append(ts, consultTools(ctx, eng, opts, who)...)
	}
	return ts
}

// refusal turns a consult refusal into something the model can act on.
func refusal(err error) map[string]any {
	return map[string]any{"refused": string(consult.RuleOf(err)), "reason": err.Error()}
}

func consultTools(ctx context.Context, eng *engine.Engine, opts Options, who func(context.Context) governance.Principal) []tool {
	svc := opts.Consult
	actionHelp := "No external actions are configured, so plans carry no actions."
	if opts.Remote != nil {
		var lines []string
		for _, a := range opts.Remote.ActionSpecs(ctx) {
			line := fmt.Sprintf("server=%q tool=%q", a.Server, a.Tool)
			if a.Description != "" {
				line += ": " + a.Description
			}
			if a.Schema != nil {
				if b, err := json.Marshal(a.Schema); err == nil {
					line += " args schema " + string(b)
				}
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			actionHelp = "Actions a plan may carry (they run only after a person adopts the plan): " + strings.Join(lines, "; ")
		}
	}
	return []tool{
		{name: "consult_list", readOnly: true,
			desc:   "List the consulting record: goals (with server-measured baselines), findings, plans and their states (proposed/adopted/rejected/accepted), due dates and verdicts.",
			schema: obj(map[string]any{}),
			handler: func(ctx context.Context, _ map[string]any) (any, error) {
				b, err := svc.Board(ctx)
				if err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				return summarize(b), nil
			}},
		{name: "consult_add_goal",
			desc: "Record a goal: metric (must exist), direction, relative magnitude (0.1 = 10%), deadline in days, and scope as dimension filters. The server measures the baseline over the trailing window; you do not supply it.",
			schema: obj(map[string]any{
				"what": str("the goal in one sentence"), "metric": str("metric name"),
				"direction":   map[string]any{"type": "string", "enum": []string{"up", "down"}},
				"by":          num("relative magnitude, e.g. 0.1 for 10%"),
				"within_days": map[string]any{"type": "integer"},
				"scope":       filtersSchema, "time_dimension": str("optional"), "guards": guardsSchema,
			}, "what", "metric", "direction", "by", "within_days"),
			handler: func(ctx context.Context, a map[string]any) (any, error) {
				var in consult.GoalInput
				if err := remarshal(a, &in); err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				g, err := svc.AddGoal(ctx, in, who(ctx), "copilot")
				if err != nil {
					return refusal(err), nil
				}
				return map[string]any{"goal": g.ID, "baseline": g.Baseline, "target": g.Target()}, nil
			}},
		{name: "consult_add_finding",
			desc: "Record a finding: one sentence plus at least one semantic query that supports it. The server executes each query through governance and stores the SQL, rows and row count as evidence; a finding without evidence, or whose query fails, is refused.",
			schema: obj(map[string]any{
				"goal": str("goal id, e.g. g1 (optional)"), "says": str("the finding, one sentence"),
				"evidence": map[string]any{"type": "array", "items": obj(map[string]any{
					"asked": str("what this query shows"), "query": querySchema,
				}, "asked", "query")},
			}, "says", "evidence"),
			handler: func(ctx context.Context, a map[string]any) (any, error) {
				in := consult.FindingInput{Goal: optString(a["goal"]), Says: optString(a["says"])}
				items, _ := a["evidence"].([]any)
				for _, it := range items {
					m, _ := it.(map[string]any)
					qm, _ := m["query"].(map[string]any)
					q, err := agenttools.ParseQuery(eng.Model, qm, time.Now())
					if err != nil {
						return map[string]any{"error": err.Error()}, nil
					}
					in.Evidence = append(in.Evidence, consult.EvidenceInput{Asked: optString(m["asked"]), Query: q})
				}
				f, err := svc.AddFinding(ctx, in, who(ctx), "copilot")
				if err != nil {
					return refusal(err), nil
				}
				ev := make([]map[string]any, 0, len(f.Evidence))
				for _, e := range f.Evidence {
					ev = append(ev, map[string]any{"asked": e.Asked, "row_count": e.RowCount, "value": e.Value, "sql": e.SQL})
				}
				return map[string]any{"finding": f.ID, "evidence": ev}, nil
			}},
		{name: "consult_propose_plan",
			desc: "Propose a plan. It binds at least one finding, states the expected effect (metric, direction, magnitude, window in days, optional scope — defaults to the goal's), guards, and optional actions. Proposing does not adopt or run anything; a different person adopts it in the console. " + actionHelp,
			schema: obj(map[string]any{
				"goal": str("goal id (optional)"), "findings": strs("finding ids, at least one"),
				"does": str("what to do, one or two sentences"),
				"expect": obj(map[string]any{
					"metric": str("metric"), "direction": map[string]any{"type": "string", "enum": []string{"up", "down"}},
					"by": num("relative magnitude"), "within_days": map[string]any{"type": "integer"},
					"scope": filtersSchema, "time_dimension": str("optional"),
				}, "metric", "direction", "by", "within_days"),
				"guards": guardsSchema,
				"actions": map[string]any{"type": "array", "items": obj(map[string]any{
					"server": str("configured server name"), "tool": str("configured action tool"),
					"args": map[string]any{"type": "object"}, "why": str("why this action"),
				}, "server", "tool")},
				"cost": str("rough cost in money, people or time"),
			}, "findings", "does", "expect"),
			handler: func(ctx context.Context, a map[string]any) (any, error) {
				var in consult.PlanInput
				if err := remarshal(a, &in); err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				p, err := svc.ProposePlan(ctx, in, who(ctx), "copilot")
				if err != nil {
					return refusal(err), nil
				}
				return map[string]any{"plan": p.ID, "state": "proposed", "note": p.Note,
					"next": "a person other than " + p.By + " must adopt it in the console; you cannot"}, nil
			}},
	}
}

func optString(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}

func remarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// summarize keeps consult_list small enough for a model's context.
func summarize(b *consult.Board) map[string]any {
	plan := func(v consult.PlanView) map[string]any {
		m := map[string]any{"id": v.Plan.ID, "does": v.Plan.Does, "state": v.State, "findings": v.Plan.Findings,
			"expect": v.Plan.Expect, "by": v.Plan.By}
		if v.Due != "" {
			m["due"] = v.Due
		}
		if v.Decision != nil {
			m["decided_by"] = v.Decision.Who
			if v.Decision.Baseline != nil {
				m["baseline"] = v.Decision.Baseline.Value
			}
		}
		if v.Acceptance != nil {
			m["outcome"], m["says"] = v.Acceptance.Outcome, v.Acceptance.Says
		} else if v.Progress != nil {
			m["progress"] = v.Progress.Says
		}
		return m
	}
	var goals []map[string]any
	for _, g := range b.Goals {
		var fs []map[string]any
		for _, f := range g.Findings {
			fs = append(fs, map[string]any{"id": f.ID, "says": f.Says, "evidence": len(f.Evidence)})
		}
		var ps []map[string]any
		for _, p := range g.Plans {
			ps = append(ps, plan(p))
		}
		gm := map[string]any{"id": g.Goal.ID, "what": g.Goal.What, "metric": g.Goal.Metric, "direction": g.Goal.Direction,
			"by": g.Goal.By, "within_days": g.Goal.WithinDays, "scope": g.Goal.Scope, "target": g.Target,
			"findings": fs, "plans": ps}
		if g.Goal.Baseline != nil {
			gm["baseline"] = g.Goal.Baseline.Value
		}
		goals = append(goals, gm)
	}
	out := map[string]any{"goals": goals}
	if len(b.LooseFindings) > 0 {
		var fs []map[string]any
		for _, f := range b.LooseFindings {
			fs = append(fs, map[string]any{"id": f.ID, "says": f.Says, "evidence": len(f.Evidence)})
		}
		out["findings_without_goal"] = fs
	}
	if len(b.LoosePlans) > 0 {
		var ps []map[string]any
		for _, p := range b.LoosePlans {
			ps = append(ps, plan(p))
		}
		out["plans_without_goal"] = ps
	}
	return out
}
