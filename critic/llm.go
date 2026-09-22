package critic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/llm"

	"github.com/liliang-cn/dataintelligence/engine"
)

// LLMCritic catches the semantic failures rules can't see — wrong grain, subtly
// wrong metric identity, an answer that doesn't actually address the question. It
// inspects the question, the chosen metrics' definitions, and a sample of rows.
type LLMCritic struct{ svc *llm.Service }

// NewLLMCritic wires an agent-go LLM from LLM_* env. Returns (nil, err) when
// unconfigured so callers can fall back to the rule critic alone.
func NewLLMCritic() (*LLMCritic, error) {
	svc, err := llm.NewOpenAIFromEnv()
	if err != nil {
		return nil, err
	}
	return &LLMCritic{svc: svc}, nil
}

func (c *LLMCritic) Critique(ctx context.Context, in Input) Verdict {
	var b strings.Builder
	b.WriteString(`You are a strict data-analytics critic. Judge whether the RESULT correctly answers the QUESTION.
Check four dimensions: grain (right aggregation level, no fan-out), coverage (every clause answered),
sanity (values plausible), metric-identity (the metric used is the one asked for).
Return STRICT JSON: {"decision":"pass|revise|ask_user","dimension":"","reasons":["..."],"feedback":"one actionable fix"}.
Use "pass" only if all four hold. Use "ask_user" only if the question is genuinely ambiguous. Otherwise "revise".

QUESTION: ` + in.Question + "\n\nMETRICS USED:\n")
	for _, name := range in.Query.Metrics {
		if m := in.Model.Metric(name); m != nil {
			fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	fmt.Fprintf(&b, "GROUP BY: %v\nTIME GRAIN: %q\n", in.Query.GroupBy, in.Query.TimeGrain)
	b.WriteString("\nRESULT (sample):\n")
	b.WriteString(sampleRows(in.Answer, 5))
	b.WriteString("\nJSON:")

	raw, err := c.svc.Ask(ctx, b.String())
	if err != nil {
		// A critic that can't run shouldn't block the answer — defer to rules.
		return Verdict{Decision: Pass, By: "llm", Reasons: []string{"llm critic error: " + err.Error()}}
	}
	js, err := extractJSON(raw)
	if err != nil {
		return Verdict{Decision: Pass, By: "llm", Reasons: []string{"llm critic non-JSON"}}
	}
	var out struct {
		Decision  string   `json:"decision"`
		Dimension string   `json:"dimension"`
		Reasons   []string `json:"reasons"`
		Feedback  string   `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return Verdict{Decision: Pass, By: "llm", Reasons: []string{"llm critic parse error"}}
	}
	d := Decision(strings.ToLower(strings.TrimSpace(out.Decision)))
	if d != Pass && d != Revise && d != AskUser {
		d = Pass
	}
	fb := out.Feedback
	if fb == "" {
		fb = strings.Join(out.Reasons, "; ")
	}
	return Verdict{Decision: d, Dimension: out.Dimension, Reasons: out.Reasons, Feedback: fb, By: "llm"}
}

// Chain runs the cheap rule critic first; only if it passes does it consult the
// (slower, paid) LLM critic. The first non-pass verdict wins.
type Chain struct {
	Rule Critic
	LLM  Critic // may be nil
}

func (c Chain) Critique(ctx context.Context, in Input) Verdict {
	if c.Rule != nil {
		if v := c.Rule.Critique(ctx, in); v.Decision != Pass {
			return v
		}
	}
	if c.LLM != nil {
		return c.LLM.Critique(ctx, in)
	}
	return Verdict{Decision: Pass, By: "rule"}
}

// sampleRows renders up to n rows of the answer for the critic prompt.
func sampleRows(ans *engine.Answer, n int) string {
	if ans == nil || len(ans.Rows) == 0 {
		return "(no rows)"
	}
	var b strings.Builder
	b.WriteString(strings.Join(ans.Columns, " | "))
	b.WriteByte('\n')
	for i, row := range ans.Rows {
		if i >= n {
			fmt.Fprintf(&b, "... (%d more rows)\n", len(ans.Rows)-n)
			break
		}
		cells := make([]string, len(row))
		for j, c := range row {
			cells[j] = fmt.Sprintf("%v", c)
		}
		b.WriteString(strings.Join(cells, " | "))
		b.WriteByte('\n')
	}
	return b.String()
}

func extractJSON(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", errors.New("no JSON object")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("unbalanced JSON")
}
