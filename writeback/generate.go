package writeback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/llm"
)

// Generator turns a natural-language request into a typed Proposal, grounded on
// the write-back allowlist. It emits STRUCTURED JSON (never SQL); the result is
// then validated + dry-run by the engine before anything is persisted.
type Generator struct {
	svc    *llm.Service
	schema *Schema
}

// NewGenerator wires the agent-go LLM from env. Returns (nil, err) when no creds.
func NewGenerator(s *Schema) (*Generator, error) {
	svc, err := llm.NewOpenAIFromEnv()
	if err != nil {
		return nil, err
	}
	return &Generator{svc: svc, schema: s}, nil
}

// Generate produces a Proposal for a data change. modelCatalog (optional) lets
// the model propose transforms too when kind=model.
func (g *Generator) Generate(ctx context.Context, question, modelCatalog string) (*Proposal, error) {
	var b strings.Builder
	b.WriteString(`Turn the user's request into ONE change proposal as STRICT JSON. Output ONLY JSON.
Shape:
{"kind":"data|model","rationale":"...",
 "data":{"op":"insert|update|delete","table":"...","set":{"col":val},"where":[{"column":"...","operator":"=","value":...}]},
 "model":{"kind":"metric|dimension","name":"...","yaml":"<a single YAML list item>"}}
Rules:
- Use ONLY the writable tables/columns/operations below; never invent names; never output SQL.
- update/delete MUST include a "where" that targets the intended rows (usually the primary key).
- For "model" transforms, "yaml" is one metric/dimension entry (e.g. "- name: avg_refund\n  formula: refund_total / order_count").

WRITABLE TABLES:
`)
	for i := range g.schema.Tables {
		t := &g.schema.Tables[i]
		ops := []string{}
		if t.AllowInsert {
			ops = append(ops, "insert")
		}
		if t.AllowUpdate {
			ops = append(ops, "update")
		}
		if t.AllowDelete {
			ops = append(ops, "delete")
		}
		fmt.Fprintf(&b, "- %s (pk=%s, ops=%s): ", t.Name, t.PrimaryKey, strings.Join(ops, ","))
		var cs []string
		for _, c := range t.Columns {
			s := c.Name + ":" + c.Type
			if len(c.Enum) > 0 {
				s += "{" + strings.Join(c.Enum, "|") + "}"
			}
			cs = append(cs, s)
		}
		b.WriteString(strings.Join(cs, ", "))
		b.WriteByte('\n')
	}
	if modelCatalog != "" {
		b.WriteString("\nEXISTING METRICS/DIMENSIONS (for model transforms; reference by name):\n")
		b.WriteString(modelCatalog)
	}
	fmt.Fprintf(&b, "\nREQUEST: %s\nJSON:", question)

	raw, err := g.svc.Ask(ctx, b.String())
	if err != nil {
		return nil, err
	}
	js, err := extractJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("generator returned non-JSON: %w", err)
	}
	var prop Proposal
	if err := json.Unmarshal([]byte(js), &prop); err != nil {
		return nil, fmt.Errorf("parse proposal: %w", err)
	}
	prop.Question = question
	if prop.Kind == "" {
		if prop.Model != nil {
			prop.Kind = "model"
		} else {
			prop.Kind = "data"
		}
	}
	if prop.Kind == "data" && prop.Data == nil {
		return nil, fmt.Errorf("generator produced no data change")
	}
	return &prop, nil
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
