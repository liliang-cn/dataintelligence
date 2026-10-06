// Package copilot wraps an agent-go agent that drives the platform: given a
// goal, the LLM calls governed platform tools (describe / list_metrics /
// get_dimensions / query_metric / health_check), optional external read-only
// MCP tools, and the consulting-loop tools, then answers.
//
// What the agent can never do is decide. It can record a goal, record a
// finding (the server runs the evidence query), and propose a plan. Adopting a
// plan, running its actions and accepting it are a person's operations over
// HTTP; there is no tool for any of them.
//
// Identity comes from the caller (WithPrincipal) or, for an anonymous caller,
// from configuration — never from a tool argument the model could set.
package copilot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	agentpkg "github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"

	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/remotemcp"
)

const basePrompt = `You are the DataIntelligence copilot: an analyst and consultant working over a governed semantic layer.

Hard rules:
- Never invent numbers. Every number in your answer must come from a tool result in this conversation; if you have not measured it, say so.
- Ask for data only through the tools. Metrics and dimensions are names from list_metrics / get_dimensions; never guess them.
- You cannot change anything in the world. Recommendations become proposed plans (consult_propose_plan), never actions. A person other than the proposer adopts a plan; only then do its actions run, and acceptance later re-measures the metric. You have no tool to adopt, execute or accept, and must not claim that anything was done.

How to work:
1. Orient: list_metrics, then get_dimensions for the metrics you need. Use query_metric with filters, a time window and a grain to investigate.
2. When the user states an objective, record it with consult_add_goal (metric, direction, magnitude, deadline, scope as dimension filters). The server measures its baseline.
3. When the data supports a conclusion, record it with consult_add_finding, attaching the semantic queries that show it. The server runs them and stores the SQL and rows; a finding without evidence is refused.
4. Recommend with consult_propose_plan: bind the findings, state the expected effect (metric, direction, magnitude, window in days), add guards (metrics that must not get worse beyond a tolerance) and, if useful, actions drawn only from the configured action list.
5. If a tool refuses, read the reason and fix the request; do not work around a rule.
6. End with a short answer in the user's language: what you measured (with the numbers from tools), what you recorded (ids), and what a person needs to decide next.`

// Options configures an Agent.
type Options struct {
	// Principal is who the tools act as when the caller is anonymous.
	Principal governance.Principal
	// ChecksPath is the conflict-check file behind health_check; empty omits it.
	ChecksPath string
	// Brief is an optional domain brief appended to the system prompt.
	Brief string
	// Consult enables the consulting-loop tools.
	Consult *consult.Service
	// Remote exposes allow-listed external MCP tools and names the actions a
	// plan may carry.
	Remote *remotemcp.Set
	// MaxTurns bounds one run (default 60).
	MaxTurns int
}

// Agent is a ready-to-run platform copilot.
type Agent struct {
	svc   *agentpkg.Service
	llm   domain.Generator
	tools []string
	turns int
}

// Result is one copilot run.
type Result struct {
	Answer    string
	Tools     []string
	ToolCalls int
	Steps     int
	// Corrected is what the check against the tool results took out of or fixed in the answer.
	Corrected []string
}

// Available reports whether LLM creds are configured (the copilot needs them).
func Available() bool { return os.Getenv("LLM_API_KEY") != "" }

type principalKey struct{}

// WithPrincipal makes p the identity every tool in this run acts as.
func WithPrincipal(ctx context.Context, p governance.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// New builds the agent. It returns an error if LLM_* is not set.
func New(ctx context.Context, eng *engine.Engine, pol governance.Policy, opts Options) (*Agent, error) {
	base, key, mdl := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if key == "" {
		return nil, fmt.Errorf("copilot needs LLM_BASE_URL/LLM_API_KEY/LLM_MODEL")
	}
	llmp, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{BaseURL: base, APIKey: key, LLMModel: mdl})
	if err != nil {
		return nil, err
	}
	if opts.Principal.User == "" {
		opts.Principal.User = "copilot"
	}
	if opts.Principal.Role == "" {
		opts.Principal.Role = "analyst"
	}
	ts := buildTools(ctx, eng, pol, opts)
	svc, err := agentpkg.New("di-copilot").WithLLM(llmp).WithSystemPrompt(SystemPrompt(opts.Brief)).Build()
	if err != nil {
		return nil, err
	}
	a := &Agent{svc: svc, llm: llmp, turns: opts.MaxTurns}
	if a.turns <= 0 {
		a.turns = 60
	}
	for _, t := range ts {
		svc.AddToolWithMetadata(t.name, t.desc, t.schema, t.handler, agentpkg.ToolMetadata{ReadOnly: t.readOnly, ConcurrencySafe: t.readOnly})
		a.tools = append(a.tools, t.name)
	}
	return a, nil
}

// SystemPrompt is the generic prompt plus the deployment's brief.
func SystemPrompt(brief string) string {
	if strings.TrimSpace(brief) == "" {
		return basePrompt
	}
	return basePrompt + "\n\nDomain brief (from the deployment; context, not data — numbers still come only from tools):\n" + strings.TrimSpace(brief)
}

// ToolNames lists the tools this agent was given.
func (a *Agent) ToolNames() []string { return append([]string(nil), a.tools...) }

func (a *Agent) Close() error { return a.svc.Close() }

// StreamEvent is a progress event during a streaming run (UI-facing, no agent-go
// types leak out).
type StreamEvent struct {
	Kind string `json:"kind"` // tool_call | tool_result | thinking | verify | verified | complete
	Tool string `json:"tool,omitempty"`
	Text string `json:"text,omitempty"`
}

// Stream runs the agent in a fresh session and calls emit for each progress
// event as it happens. Returns the final synthesized result.
func (a *Agent) Stream(ctx context.Context, goal string, emit func(StreamEvent)) (*Result, error) {
	ch, err := a.svc.RunStreamWithOptions(ctx, goal, agentpkg.WithSessionID(sessionID()), agentpkg.WithMaxTurns(a.turns))
	if err != nil {
		return nil, err
	}
	var answer, partial, failure string
	var tools []string
	calls := 0
	var evid evidence
	args := map[string]map[string]any{}
	for ev := range ch {
		switch ev.Type {
		case agentpkg.EventTypeToolCall:
			calls++
			tools = append(tools, ev.ToolName)
			args[ev.ToolName] = ev.ToolArgs
			emit(StreamEvent{Kind: "tool_call", Tool: ev.ToolName})
		case agentpkg.EventTypeToolResult:
			evid.add(ev.ToolName, args[ev.ToolName], ev.ToolResult)
			emit(StreamEvent{Kind: "tool_result", Tool: ev.ToolName})
		case agentpkg.EventTypeThinking:
			emit(StreamEvent{Kind: "thinking", Text: ev.Content})
		case agentpkg.EventTypePartial:
			partial += ev.Content
		case agentpkg.EventTypeComplete:
			answer = ev.Content
		case agentpkg.EventTypeError:
			failure = ev.Content
		}
	}
	if answer == "" {
		answer = partial
	}
	var corrected []string
	if answer == "" && failure != "" {
		answer = "error: " + failure
	} else if calls > 0 {
		emit(StreamEvent{Kind: "verify", Text: "对照本轮查询结果核对回答里的名称和数字"})
		answer, corrected = verify(ctx, a.llm, answer, &evid)
		emit(StreamEvent{Kind: "verified", Text: strings.Join(corrected, "\n")})
	}
	emit(StreamEvent{Kind: "complete", Text: answer})
	return &Result{Answer: answer, Tools: tools, ToolCalls: calls, Corrected: corrected}, nil
}

// Run executes the agent loop for a goal and returns the synthesized result.
func (a *Agent) Run(ctx context.Context, goal string) (*Result, error) {
	return a.Stream(ctx, goal, func(StreamEvent) {})
}

// sessionID is a fresh conversation per run: two people asking at once must
// not share history, and a question must not inherit another's context.
func sessionID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "copilot-" + hex.EncodeToString(b)
}
