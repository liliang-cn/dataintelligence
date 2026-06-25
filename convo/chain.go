package convo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
)

// A multi-step question ("refunds for the top-revenue region") decomposes into
// ordered steps where a later step filters on an earlier step's result. The
// planner emits the steps with explicit dependency edges; the executor resolves
// each ${stepID} reference, runs every metric as its own governed call, and
// caches sub-results so a repeated step is free.

// Step is one node of a chained plan.
type Step struct {
	ID      string   `json:"id"`
	Metrics []string `json:"metrics"`
	GroupBy []string `json:"group_by,omitempty"`
	Where   []Ref    `json:"where,omitempty"`
	Pick    *Pick    `json:"pick,omitempty"` // extract a dimension value for downstream steps
}

// Ref is a filter whose value may reference a prior step's pick via "${stepID}".
type Ref struct {
	Dimension string `json:"dimension"`
	Op        string `json:"op"`
	Value     string `json:"value"`
}

// Pick selects one dimension value from a step's result (the top/bottom row by a
// metric) to feed the next step's filter.
type Pick struct {
	By        string `json:"by"`        // metric to rank by
	Dir       string `json:"dir"`       // top | bottom
	Dimension string `json:"dimension"` // dimension whose value is carried forward
}

// StepResult records one executed step.
type StepResult struct {
	Step   Step
	Query  semantic.Query
	Answer *engine.Answer
	Picked string // the value Pick extracted (if any)
}

// ChainResult is the outcome of a chained question.
type ChainResult struct {
	Question string
	Steps    []StepResult
	Final    *engine.Answer // the last step's answer
	Note     string
}

// Plan asks the LLM to decompose a question into ordered steps. It returns
// (nil, false, nil) when there's no LLM or the question is single-step.
func (s *Session) Plan(ctx context.Context, question string) ([]Step, bool, error) {
	var b strings.Builder
	b.WriteString(`Decompose the analytics question into ORDERED steps ONLY if a later step must filter on an
earlier step's result (e.g. "metric B for the top region by metric A"). Otherwise return {"steps":[]}.
STRICT JSON: {"steps":[{"id":"s1","metrics":["..."],"group_by":["..."],"pick":{"by":"<metric>","dir":"top|bottom","dimension":"<dim>"}},
{"id":"s2","metrics":["..."],"where":[{"dimension":"<dim>","op":"=","value":"${s1}"}]}]}
Use ONLY catalog names. A step that needs no upstream value has no "where" ref. Max 3 steps.

METRICS:
`)
	for i := range s.Eng.Model.Metrics {
		m := &s.Eng.Model.Metrics[i]
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
	}
	b.WriteString("DIMENSIONS:\n")
	for i := range s.Eng.Model.Dimensions {
		d := &s.Eng.Model.Dimensions[i]
		fmt.Fprintf(&b, "- %s (%s)\n", d.Name, d.Type)
	}
	fmt.Fprintf(&b, "\nQUESTION: %s\nJSON:", question)

	raw, ok, err := s.Gr.AskLLM(ctx, b.String())
	if err != nil || !ok {
		return nil, false, err
	}
	js, err := extractJSON(raw)
	if err != nil {
		return nil, false, err
	}
	var out struct {
		Steps []Step `json:"steps"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, false, err
	}
	return out.Steps, len(out.Steps) > 1, nil
}

// RunChain plans and executes a multi-step question. If the question isn't
// multi-step it returns (nil, false, nil) so the caller falls back to Ask.
func (s *Session) RunChain(ctx context.Context, question string) (*ChainResult, bool, error) {
	steps, multi, err := s.Plan(ctx, question)
	if err != nil || !multi {
		return nil, false, err
	}
	res := &ChainResult{Question: question}
	picks := map[string]string{}

	for _, st := range steps {
		q := semantic.Query{Metrics: st.Metrics, GroupBy: st.GroupBy}
		for _, ref := range st.Where {
			val := resolve(ref.Value, picks)
			if val == "" {
				return res, true, fmt.Errorf("step %s: unresolved reference %q", st.ID, ref.Value)
			}
			op := ref.Op
			if op == "" {
				op = "="
			}
			q.Where = append(q.Where, semantic.Filter{Dimension: ref.Dimension, Op: op, Values: []any{val}})
		}
		ans, qerr := s.runCached(ctx, q)
		if qerr != nil {
			res.Note = fmt.Sprintf("step %s refused/failed: %v", st.ID, qerr)
			return res, true, nil
		}
		sr := StepResult{Step: st, Query: q, Answer: ans}
		if st.Pick != nil {
			sr.Picked = pickValue(ans, st.Pick)
			picks[st.ID] = sr.Picked
		}
		res.Steps = append(res.Steps, sr)
		res.Final = ans
	}
	return res, true, nil
}

func resolve(v string, picks map[string]string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return picks[strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")]
	}
	return v
}

// pickValue returns the dimension value of the top/bottom row by the pick metric.
func pickValue(ans *engine.Answer, p *Pick) string {
	di, mi := -1, -1
	for i, c := range ans.Columns {
		if c == p.Dimension {
			di = i
		}
		if c == p.By {
			mi = i
		}
	}
	if di < 0 || mi < 0 || len(ans.Rows) == 0 {
		return ""
	}
	rows := append([][]any(nil), ans.Rows...)
	sort.SliceStable(rows, func(a, b int) bool {
		if p.Dir == "bottom" {
			return toFloat(rows[a][mi]) < toFloat(rows[b][mi])
		}
		return toFloat(rows[a][mi]) > toFloat(rows[b][mi])
	})
	return fmt.Sprintf("%v", rows[0][di])
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	default:
		var f float64
		_, _ = fmt.Sscanf(fmt.Sprintf("%v", t), "%g", &f)
		return f
	}
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
