package mcp

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/dataintelligence/brief"
	"github.com/liliang-cn/dataintelligence/governance"
)

// The tool an agent needs and the other six do not give it.
//
// query_metric returns a number. An agent that reads one has no way to know
// whether the definition behind it was ever approved, or what was written about
// why it is defined that way — so it either states the figure as fact or hedges
// every figure equally, and both are wrong. The second is what agents actually
// do, and it is why a correct number out of a warehouse still gets argued with.
//
// brief returns the figure together with the two things that settle the
// argument: the name of whoever approved the definition that produced it, and
// the passages somebody wrote about that definition. An agent can then say
// "revenue was 122, net of refunds, a definition 张三 approved on the 14th",
// which is a sentence a reader can check.
//
// The tool deliberately reports an unapproved definition rather than refusing
// to answer with one. Refusing would make the tool unusable during the weeks a
// model is being built, which is when an agent is most useful; saying so in the
// payload lets the agent decide how much weight the figure carries.

type briefIn struct {
	Question string `json:"question" jsonschema:"the question, in natural language"`
	Passages int    `json:"passages,omitempty" jsonschema:"how many supporting passages to cite (default 3)"`
}

func (s *srv) brief(ctx context.Context, req *mcpsdk.CallToolRequest, in briefIn) (*mcpsdk.CallToolResult, any, error) {
	p, deny := s.guard(req, "metrics:read")
	if deny != nil {
		return deny, nil, nil
	}
	if s.opts.Grounder == nil {
		return errResult("brief is not configured (no grounding engine)"), nil, nil
	}
	if strings.TrimSpace(in.Question) == "" {
		return errResult("question is required"), nil, nil
	}
	b, err := brief.Answer(ctx, in.Question, brief.Options{
		Engine:   s.eng,
		Grounder: s.opts.Grounder,
		Corpus:   s.opts.Corpus,
		Registry: s.opts.Registry,
		Passages: in.Passages,
		// The brief runs as the real caller, like query_metric: the figure it
		// carries went through RBAC, masking and the audit trail under this
		// principal's name, not the server's.
		Who:    governance.Principal{User: p.User, Role: p.Role, Question: in.Question},
		Policy: governance.DefaultPolicy(),
	})
	if err != nil {
		return errResult(err.Error()), nil, nil
	}

	passages := make([]map[string]any, 0, len(b.Passages))
	for _, ps := range b.Passages {
		passages = append(passages, map[string]any{"document": ps.DocumentID, "text": ps.Text})
	}
	out := map[string]any{
		"question":  b.Question,
		"metrics":   b.Metrics,
		"columns":   b.Columns,
		"rows":      b.Rows,
		"sources":   b.Sources(),
		"passages":  passages,
		"model":     b.ModelHash,
		"approved":  b.Promoted,
		"signed_by": b.SignedBy,
		"signed_at": b.SignedAt,
		"reason":    b.SignNote,
	}
	if len(b.Rows) == 0 && b.NumbersBy != "" {
		out["no_figure_because"] = b.NumbersBy
	}
	// The text rendering is what a model reads; the structured payload is what
	// a program reads. Both carry the provenance, because an agent that only
	// ever sees the prose still has to be able to repeat who approved this.
	return textResult(b.Text()), out, nil
}

// briefDescription is written for the model that will decide whether to call
// this instead of query_metric, so it names the difference rather than the
// feature.
const briefDescription = "Answer a question with the figure AND its provenance: " +
	"who approved the metric definition that produced it, and what was written about that definition. " +
	"Prefer this over query_metric whenever the answer will be shown to a person, quoted, or acted on — " +
	"it is the only tool that can tell you a number came from a definition nobody approved."
