package critic

import (
	"context"
	"fmt"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
)

// Loop is the plan-query-critique driver. Each
// iteration plans (grounds) → queries (governed execute) → critiques; a revise
// verdict feeds its reason forward and retries, bounded by MaxRetries with a
// no-progress guard so it never loops or burns budget.
type Loop struct {
	Gr         *grounding.Grounder
	Eng        *engine.Engine
	Pol        governance.Policy
	Critic     Critic
	MaxRetries int // additional attempts after the first; default 2
}

// Attempt records one plan→query→critique cycle for the trace.
type Attempt struct {
	N        int
	Query    semantic.Query
	Feedback string // critic reason fed into THIS attempt's planning (empty on first)
	Verdict  Verdict
}

// Result is the loop outcome.
type Result struct {
	Outcome  string // answered | clarify | gave_up | refused
	Answer   *engine.Answer
	Clarify  *grounding.Clarify
	Attempts []Attempt
	Note     string
}

// Resolve runs the loop for a question under a principal.
func (l *Loop) Resolve(ctx context.Context, question string, p governance.Principal) (*Result, error) {
	maxR := l.MaxRetries
	if maxR <= 0 {
		maxR = 2
	}
	canRevise := strings.Contains(l.Gr.Mode(), "llm") // revising needs the LLM grounder
	res := &Result{}
	feedback := ""
	seen := map[string]bool{} // every plan we've tried — catches A→B→A oscillation

	for n := 0; n <= maxR; n++ {
		q, _, clar, err := l.Gr.GroundWithFeedback(ctx, question, feedback)
		if err != nil {
			return nil, fmt.Errorf("ground: %w", err)
		}
		if clar != nil {
			res.Outcome, res.Clarify = "clarify", clar
			return res, nil
		}

		// No-progress / cycle guard: a repeated plan (even non-consecutive) means
		// the critics disagree and the question is genuinely ambiguous — stop and ask.
		sig := signature(q)
		if n > 0 && seen[sig] {
			res.Outcome = "clarify"
			res.Clarify = &grounding.Clarify{
				Question:   "The plan is oscillating, so the question looks ambiguous. Could you rephrase or name the exact metric/dimension?",
				Candidates: lastReasons(res),
			}
			res.Note = "cycle guard tripped (repeated plan)"
			return res, nil
		}
		seen[sig] = true

		ans, qerr := governance.Query(ctx, l.Eng, q, p, l.Pol)
		if qerr != nil {
			// Governance refusal etc. — record and stop; not a retry case.
			res.Attempts = append(res.Attempts, Attempt{N: n, Query: q, Feedback: feedback,
				Verdict: Verdict{Decision: AskUser, Dimension: "governance", Reasons: []string{qerr.Error()}, By: "engine"}})
			res.Outcome, res.Note = "refused", qerr.Error()
			return res, nil
		}

		v := l.Critic.Critique(ctx, Input{Question: question, Query: q, Model: l.Eng.Model, Answer: ans})
		res.Attempts = append(res.Attempts, Attempt{N: n, Query: q, Feedback: feedback, Verdict: v})

		switch v.Decision {
		case Pass:
			res.Outcome, res.Answer = "answered", ans
			return res, nil
		case AskUser:
			res.Outcome = "clarify"
			res.Clarify = &grounding.Clarify{Question: v.Feedback, Candidates: v.Reasons}
			res.Answer = ans // keep the best-effort answer for context
			return res, nil
		case Revise:
			if !canRevise {
				// Can't auto-fix without the LLM grounder — return best effort, flagged.
				res.Outcome, res.Answer, res.Note = "gave_up", ans, "revise needed but no LLM grounder to re-plan"
				return res, nil
			}
			feedback = v.Feedback
		}
	}

	// Retries exhausted — graceful degradation: return the last answer, labeled.
	if len(res.Attempts) > 0 {
		last := res.Attempts[len(res.Attempts)-1]
		res.Outcome = "gave_up"
		res.Note = fmt.Sprintf("critic still unsatisfied after %d attempts: %s", maxR+1, last.Verdict.Feedback)
	}
	return res, nil
}

func signature(q semantic.Query) string {
	m := append([]string(nil), q.Metrics...)
	g := append([]string(nil), q.GroupBy...)
	var w []string
	for _, f := range q.Where {
		w = append(w, fmt.Sprintf("%s%s%v", f.Dimension, f.Op, f.Values))
	}
	return strings.Join(m, ",") + "|" + strings.Join(g, ",") + "|" + q.TimeGrain + "|" + strings.Join(w, ",")
}

func lastReasons(res *Result) []string {
	if len(res.Attempts) == 0 {
		return nil
	}
	return res.Attempts[len(res.Attempts)-1].Verdict.Reasons
}
