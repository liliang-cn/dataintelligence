package handover

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/rollout"
)

// The question a delivery report could not answer until now.
//
// Adoption says how many questions were asked and which metrics were used. It
// cannot say whether the definitions behind those answers were ever approved,
// because the trail records the question and the registry records the approval
// and nothing joined them. So a delivery report could state, truthfully, that a
// customer's team asked four thousand questions, while every one of them ran
// against a model file somebody edited on a laptop.
//
// This is that join, written for the person receiving the handover rather than
// for the engineer giving it: the useful number is not how many answers were
// approved, it is how many were not, and which model produced them.
//
// A deployment that never used the registry reports every answer as
// unaccounted. That is the correct reading — nothing approved them — and it is
// stated as a fact about the deployment rather than as a fault, because it is
// the ordinary state of an engagement that has not turned the registry on.

// Provenance is the answer-to-approval join over one trail.
type Provenance struct {
	Database   string                `json:"database"`
	Engagement string                `json:"engagement,omitempty"`
	Answers    int                   `json:"answers"`
	Approved   int                   `json:"approved"`
	Models     []rollout.Attestation `json:"models"`
	Registry   bool                  `json:"registry_in_use"`
}

// Attest reads the trail and the ledger and joins them on the model hash.
func Attest(ctx context.Context, eng *engine.Engine, database, engagement string) (*Provenance, error) {
	reg := rollout.New(eng.WH, func() string { return time.Now().UTC().Format(time.RFC3339) })
	models, err := reg.Attest(ctx)
	if err != nil {
		return nil, err
	}
	p := &Provenance{Database: database, Engagement: engagement, Models: models}
	for _, m := range models {
		p.Answers += m.Answers
		if m.Promoted {
			p.Approved += m.Answers
			p.Registry = true
		}
	}
	// Busiest unapproved model first: the size of the exposure is the point.
	sort.SliceStable(p.Models, func(i, j int) bool {
		if p.Models[i].Promoted != p.Models[j].Promoted {
			return !p.Models[i].Promoted
		}
		return p.Models[i].Answers > p.Models[j].Answers
	})
	return p, nil
}

// Unapproved is the count this report exists to surface.
func (p *Provenance) Unapproved() int { return p.Answers - p.Approved }

// WriteMarkdown writes the section for the delivery report.
func (p *Provenance) WriteMarkdown(w io.Writer) {
	pr := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	pr("## Where the answers came from")
	pr("")
	if p.Answers == 0 {
		pr("Nobody has asked anything yet, so there is nothing to trace. This section")
		pr("becomes useful the day the first question is asked.")
		return
	}
	if !p.Registry {
		pr("**%d answer(s), none of them from an approved definition.**", p.Answers)
		pr("")
		pr("The model registry has not been used on this deployment, so no version of")
		pr("the semantic model has been signed or promoted. Every figure anybody has")
		pr("been given came from a model file, and nothing records who agreed that the")
		pr("file was right. That is the ordinary state of an engagement that has not")
		pr("turned the registry on — and it is also exactly what a disputed number")
		pr("looks like six months later, when the person who wrote the file has gone.")
		pr("")
		pr("    di rollout register -name v1 -model <model.yaml>")
		pr("    di rollout sign     -name v1 -by <who> -note <why>")
		pr("    di rollout promote  -name v1")
		return
	}

	pr("**%d of %d answer(s)** came from a definition somebody approved.", p.Approved, p.Answers)
	if n := p.Unapproved(); n > 0 {
		pr("")
		pr("**%d did not.** Those are the figures nobody can stand behind: the model", n)
		pr("that produced them was never promoted through the registry, so there is no")
		pr("name and no date attached to the definition they used.")
	}
	pr("")
	pr("| Model | Answers | Approved by | When | Why |")
	pr("|---|---:|---|---|---|")
	for _, m := range p.Models {
		hash := m.Hash
		if hash == "" {
			hash = "_(none recorded)_"
		}
		who := m.SignedBy
		if !m.Promoted {
			who = "**nobody**"
		}
		pr("| `%s` | %d | %s | %s | %s |", hash, m.Answers, orDash(who), orDash(m.SignedAt), orDash(m.Note))
	}
}

// Summary is the line for a terminal.
func (p *Provenance) Summary() string {
	if p.Answers == 0 {
		return "no answers to trace yet"
	}
	if n := p.Unapproved(); n > 0 {
		return fmt.Sprintf("%d of %d answer(s) came from a definition nobody approved", n, p.Answers)
	}
	return fmt.Sprintf("all %d answer(s) came from an approved definition", p.Answers)
}
