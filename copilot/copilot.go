// Package copilot wraps an agent-go agent that drives the platform: given a goal,
// the LLM autonomously calls governed platform tools (describe / list_metrics /
// get_dimensions / query_metric / health_check) and synthesizes an answer plus a
// governed recommendation. Shared by the CLI (di copilot) and the web console.
package copilot

import (
	"context"
	"fmt"
	"os"
	"strings"

	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/reconcile"
)

const systemPrompt = `You are the DataIntelligence copilot for a data platform. Use the tools to fulfill the goal.
Plan: describe_warehouse to orient; health_check for data conflicts; list_metrics then get_dimensions
before query_metric; never invent numbers — only report tool results. End with a short summary and ONE
recommended next step that must go through governed propose→approve→commit (never auto-applied).`

// Agent is a ready-to-run platform copilot.
type Agent struct {
	svc *agentpkg.Service
}

// Result is one copilot run.
type Result struct {
	Answer    string
	Tools     []string
	ToolCalls int
	Steps     int
}

// Available reports whether LLM creds are configured (the copilot needs them).
func Available() bool { return os.Getenv("LLM_API_KEY") != "" }

// New builds the agent over the given engine + policy. Returns an error if LLM_*
// is not set. checksPath points at the conflict checks for health_check.
func New(eng *engine.Engine, pol governance.Policy, checksPath string) (*Agent, error) {
	base, key, mdl := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if key == "" {
		return nil, fmt.Errorf("copilot needs LLM_BASE_URL/LLM_API_KEY/LLM_MODEL")
	}
	llmp, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{BaseURL: base, APIKey: key, LLMModel: mdl})
	if err != nil {
		return nil, err
	}
	svc, err := agentpkg.New("di-copilot").
		WithLLM(llmp).WithPTC(false).WithSystemPrompt(systemPrompt).
		WithTools(tools(eng, pol, checksPath)...).
		Build()
	if err != nil {
		return nil, err
	}
	return &Agent{svc: svc}, nil
}

func (a *Agent) Close() error { return a.svc.Close() }

// Run executes the agent loop for a goal and returns the synthesized result.
func (a *Agent) Run(ctx context.Context, goal string) (*Result, error) {
	res, err := a.svc.Chat(ctx, goal)
	if err != nil {
		return nil, err
	}
	return &Result{
		Answer:    fmt.Sprintf("%v", res.FinalResult),
		Tools:     res.ToolsUsed,
		ToolCalls: res.ToolCalls,
		Steps:     res.StepsTotal,
	}, nil
}

func tools(eng *engine.Engine, pol governance.Policy, checksPath string) []*agentpkg.Tool {
	return []*agentpkg.Tool{
		agentpkg.BuildTool("describe_warehouse").
			Description("List the warehouse tables and their column counts.").
			Handler(func(ctx context.Context, _ map[string]any) (any, error) {
				s, err := modelgen.Introspect(ctx, eng.WH)
				if err != nil {
					return nil, err
				}
				out := []string{}
				for _, t := range s.Tables {
					out = append(out, fmt.Sprintf("%s (%d cols)", t.Name, len(t.Columns)))
				}
				return out, nil
			}).Build(),

		agentpkg.BuildTool("list_metrics").
			Description("List available semantic metrics with descriptions.").
			Handler(func(_ context.Context, _ map[string]any) (any, error) {
				out := []string{}
				for i := range eng.Model.Metrics {
					m := &eng.Model.Metrics[i]
					out = append(out, m.Name+": "+m.Description)
				}
				return out, nil
			}).Build(),

		agentpkg.BuildTool("get_dimensions").
			Description("Valid dimensions to group a metric by WITHOUT a fan-out. Call before query_metric.").
			Param("metric", agentpkg.TypeString, "the metric name", agentpkg.Required()).
			Handler(func(_ context.Context, a map[string]any) (any, error) {
				return eng.Model.DimensionsFor(fmt.Sprint(a["metric"]))
			}).Build(),

		agentpkg.BuildTool("query_metric").
			Description("Run a governed semantic query. Returns rows. RBAC/masking/RLS apply by role.").
			Param("metrics", agentpkg.TypeString, "comma-separated metric names", agentpkg.Required()).
			Param("group_by", agentpkg.TypeString, "comma-separated dimension names (optional)").
			Param("role", agentpkg.TypeString, "caller role: analyst|finance|manager|admin").
			Handler(func(ctx context.Context, a map[string]any) (any, error) {
				role := fmt.Sprint(a["role"])
				if role == "" || role == "<nil>" {
					role = "finance"
				}
				q := semantic.Query{Metrics: splitCSV(fmt.Sprint(a["metrics"])), GroupBy: splitCSV(fmt.Sprint(a["group_by"]))}
				ans, err := governance.Query(ctx, eng, q, governance.Principal{User: "copilot", Role: role, Attrs: map[string]string{"region": "South"}}, pol)
				if err != nil {
					return "refused by governance: " + err.Error(), nil
				}
				return map[string]any{"columns": ans.Columns, "rows": ans.Rows}, nil
			}).Build(),

		agentpkg.BuildTool("health_check").
			Description("Detect cross-source data conflicts (orphans, price drift, oversell).").
			Handler(func(ctx context.Context, _ map[string]any) (any, error) {
				cs, err := reconcile.Load(checksPath)
				if err != nil {
					return nil, err
				}
				results, err := reconcile.Run(ctx, eng.WH, cs, nil)
				if err != nil {
					return nil, err
				}
				out := []map[string]any{}
				for _, r := range results {
					out = append(out, map[string]any{"check": r.Check.Name, "severity": r.Check.Severity, "conflicts": r.Count()})
				}
				return out, nil
			}).Build(),
	}
}

func splitCSV(s string) []string {
	if s == "" || s == "<nil>" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
