package nleval

import (
	"context"
	"io"

	evalgo "github.com/liliang-cn/eval-go"
	"github.com/liliang-cn/eval-go/llmjudge"
)

// JudgeReport is the optional LLM-as-judge layer (the RAG
// faithfulness/groundedness axes). It runs only when LLM creds are present
// (LLM_BASE_URL / LLM_API_KEY / LLM_MODEL); without them the closed loop still
// grades semantic/execution/result deterministically.
type JudgeReport struct {
	Available bool
	Report    evalgo.Report
}

// Judge builds eval-go samples from the executed answers and scores groundedness:
// the answer must be non-empty, faithful to the retrieved metric definitions
// (no claims the context doesn't support), and relevant to the question.
func Judge(ctx context.Context, rep *Report) (*JudgeReport, error) {
	judge, err := llmjudge.FromEnv()
	if err != nil {
		return &JudgeReport{Available: false}, nil // no creds → skip cleanly
	}

	var samples []evalgo.Sample
	for _, c := range rep.Cases {
		if c.Skipped || c.Answer == "" || len(c.Context) == 0 {
			continue
		}
		samples = append(samples, evalgo.Sample{
			Name:    c.Case,
			Input:   c.Question,
			Output:  c.Answer,
			Context: c.Context,
			Rubric:  "The answer states the value of the business metric the question asks for, and nothing it cannot support from the context.",
			Meta:    map[string]string{"category": c.Category},
		})
	}
	if len(samples) == 0 {
		return &JudgeReport{Available: false}, nil
	}

	suite := evalgo.Suite{
		Samples: samples,
		Metrics: []evalgo.Metric{
			evalgo.NonEmpty(),
			evalgo.Faithfulness(judge),
			evalgo.AnswerRelevancy(judge, 0.5),
			evalgo.RubricJudge(judge, 0.5),
		},
		Concurrency: 4,
	}
	return &JudgeReport{Available: true, Report: suite.Run(ctx)}, nil
}

// WriteConsole prints the judge layer (or a note when it was skipped).
func (j *JudgeReport) WriteConsole(w io.Writer) {
	if !j.Available {
		io.WriteString(w, "\n--- groundedness (LLM judge) ---\n  skipped: set LLM_BASE_URL/LLM_API_KEY/LLM_MODEL to enable faithfulness + relevancy\n")
		return
	}
	io.WriteString(w, "\n--- groundedness (LLM judge) ---\n")
	j.Report.WriteConsole(w)
}
