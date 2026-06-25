// Package convo adds conversation memory and multi-metric chaining on top of the
// grounding + governance + critic stack. A Session keeps a small typed query
// state (not the transcript): each turn is a field-level merge of the prior state
// when the user refines, and a reset when the topic shifts. Sub-results are cached
// within the thread so chained steps don't re-run the same metric.
package convo

import (
	"context"
	"fmt"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/critic"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
)

// Session is one conversation thread for a single principal.
type Session struct {
	Gr     *grounding.Grounder
	Eng    *engine.Engine
	Pol    governance.Policy
	Critic critic.Critic // optional: self-verify each turn
	P      governance.Principal

	state semantic.Query            // the typed memory (~the live query)
	cache map[string]*engine.Answer // sub-result cache within the thread
	turns int
}

// New starts a session.
func New(gr *grounding.Grounder, eng *engine.Engine, pol governance.Policy, p governance.Principal) *Session {
	return &Session{Gr: gr, Eng: eng, Pol: pol, P: p, cache: map[string]*engine.Answer{}}
}

// State returns the current typed memory.
func (s *Session) State() semantic.Query { return s.state }

// TurnResult is the outcome of one conversational turn.
type TurnResult struct {
	Question string
	Kind     string // follow-up | new-topic | clarify | refused | gave_up
	State    semantic.Query
	Answer   *engine.Answer
	Clarify  *grounding.Clarify
	Note     string
}

// Ask resolves one message in the context of the running state. The first turn
// grounds fresh; later turns merge onto (or replace) the prior state. When a
// critic is set, the turn is verified and may revise once.
func (s *Session) Ask(ctx context.Context, question string) (*TurnResult, error) {
	res := &TurnResult{Question: question}

	var (
		q   semantic.Query
		cl  *grounding.Clarify
		err error
	)
	if s.turns == 0 || empty(s.state) {
		q, _, cl, err = s.Gr.Ground(ctx, question)
	} else {
		q, cl, err = s.Gr.GroundFollowup(ctx, question, s.state)
	}
	if err != nil {
		return nil, err
	}
	if cl != nil {
		res.Kind, res.Clarify = "clarify", cl
		return res, nil
	}
	res.Kind = classify(s.state, q)

	ans, qerr := s.runCached(ctx, q)
	if qerr != nil {
		res.Kind, res.Note = "refused", qerr.Error()
		return res, nil
	}

	// One critic-driven revise, if a critic is configured.
	if s.Critic != nil {
		v := s.Critic.Critique(ctx, critic.Input{Question: question, Query: q, Model: s.Eng.Model, Answer: ans})
		if v.Decision == critic.Revise {
			if rq, _, rerr := s.Gr.GroundFollowup(ctx, question+" ("+v.Feedback+")", s.state); rerr == nil && !empty(rq) {
				if ra, raerr := s.runCached(ctx, rq); raerr == nil {
					q, ans = rq, ra
				}
			}
		} else if v.Decision == critic.AskUser {
			res.Kind = "clarify"
			res.Clarify = &grounding.Clarify{Question: v.Feedback, Candidates: v.Reasons}
			res.Answer = ans
			s.commit(q)
			return res, nil
		}
	}

	s.commit(q)
	res.State, res.Answer = q, ans
	s.turns++
	return res, nil
}

func (s *Session) commit(q semantic.Query) { s.state = q }

// runCached runs a governed query, caching by (role, query signature) within the thread.
func (s *Session) runCached(ctx context.Context, q semantic.Query) (*engine.Answer, error) {
	key := s.P.Role + "|" + sig(q)
	if a, ok := s.cache[key]; ok {
		return a, nil
	}
	a, err := governance.Query(ctx, s.Eng, q, s.P, s.Pol)
	if err != nil {
		return nil, err
	}
	s.cache[key] = a
	return a, nil
}

// empty reports whether a query has no metrics (nothing to run/merge onto).
func empty(q semantic.Query) bool { return len(q.Metrics) == 0 }

// classify labels a turn as a refinement of, or a replacement for, the prior state.
func classify(prior, next semantic.Query) string {
	if empty(prior) {
		return "new-topic"
	}
	for _, m := range next.Metrics {
		for _, pm := range prior.Metrics {
			if m == pm {
				return "follow-up"
			}
		}
	}
	return "new-topic"
}

func sig(q semantic.Query) string {
	var w []string
	for _, f := range q.Where {
		w = append(w, fmt.Sprintf("%s%s%v", f.Dimension, f.Op, f.Values))
	}
	return strings.Join(q.Metrics, ",") + "|" + strings.Join(q.GroupBy, ",") + "|" + q.TimeGrain + "|" + strings.Join(w, ",")
}
