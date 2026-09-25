// Package brief answers one question from both halves of the product at once.
//
// The warehouse half returns a number. The corpus half returns what somebody
// wrote about what that number means. The registry says who approved the
// definition that produced it. Separately each is available already and each is
// incomplete: a figure with no definition behind it is a number somebody will
// dispute, and a memo with no figure beside it is an opinion.
//
// A brief is the three together, and it is the only thing in this repository
// that a warehouse alone cannot produce. Everything else here competes with SQL
// — and loses, correctly, on latency — because everything else here is
// ultimately a nicer way to write a query. This is not: no query says who
// decided that refunds come out of revenue, and no document says what revenue
// was last quarter.
//
// # What a brief refuses to do
//
// It does not write prose. There is no summarisation step and no model asked to
// reconcile the number with the passage, because that step is where a reader
// stops being able to tell which part came from the database. The figure is the
// figure, the passage is quoted, the signature is named, and the connecting
// sentence is the reader's to write. That is a deliberate loss of polish: a
// generated paragraph would read better and be worth less, since its whole
// value would rest on a step nobody can audit.
//
// It also does not fail when half the answer is missing. A question with no
// metric behind it still returns its passages; a question nothing was written
// about still returns its number. Both halves empty is a brief that says so.
package brief

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/rollout"
	semantic "github.com/liliang-cn/semantic-go"
)

// Brief is one question answered from both sides.
type Brief struct {
	Question string

	// The warehouse half.
	Query     semantic.Query
	Metrics   []string
	Columns   []string
	Rows      [][]any
	SQL       string
	NumbersBy string // why there are no numbers, when there are none

	// Who stands behind the definition that produced them.
	ModelHash string
	SignedBy  string
	SignedAt  string
	SignNote  string
	Promoted  bool
	Attested  bool

	// The corpus half.
	Passages []corpus.Passage

	// candidates are the metrics the grounder offered when it could not pick
	// one. They are not an answer, but they are what the question is about, and
	// the corpus uses them.
	candidates []string
}

// Sources is what a brief was able to draw on: "warehouse", "corpus", both, or
// neither. It exists so a caller can tell the difference between an answer that
// is thin because the question is narrow and one that is thin because half the
// product is not configured.
func (b *Brief) Sources() []string {
	var out []string
	if len(b.Rows) > 0 {
		out = append(out, "warehouse")
	}
	if len(b.Passages) > 0 {
		out = append(out, "corpus")
	}
	return out
}

// Options are the parts a brief draws on. Any of them may be nil, and the brief
// reports what it could and could not reach rather than refusing.
type Options struct {
	Engine   *engine.Engine
	Grounder *grounding.Grounder
	Corpus   *corpus.Store
	Registry *rollout.Registry
	Who      governance.Principal
	Policy   governance.Policy
	Passages int // how many passages to cite; 0 → 4
}

// Answer runs both halves.
//
// The two are independent, and a failure on one side is recorded rather than
// returned: the most common deployment has a warehouse and no corpus, and
// making that configuration an error would mean the fused command is the one
// nobody can run.
func Answer(ctx context.Context, question string, o Options) (*Brief, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("brief: ask something")
	}
	b := &Brief{Question: question}

	if o.Engine != nil && o.Engine.Governed() && o.Grounder != nil {
		b.numbers(ctx, question, o)
	} else {
		b.NumbersBy = "no semantic model is loaded, so there is nothing to compute"
	}
	if o.Corpus != nil {
		// The corpus is asked about what the question resolved to, not only
		// about how it was worded. See corpus/about.go: a definition memo filed
		// under this metric is the answer even when it never repeats the
		// asker's phrasing.
		passages, err := o.Corpus.RecallAbout(ctx,
			corpus.About{Question: question, Metrics: b.about(o)}, o.Passages)
		if err != nil {
			return nil, err
		}
		b.Passages = passages
	}
	return b, nil
}

// about is what the model knows about the metrics this question resolved to.
//
// It reads them off the model rather than off the query, because the query
// carries names and the corpus needs vocabulary: the synonyms are there
// precisely so that people can ask in their own words, and the description is
// usually the definition itself.
//
// Metrics are resolved even when the figure failed. A question the grounder
// could not turn into a query still names a metric often enough — "why does
// revenue exclude refunds" grounds to nothing and is entirely about revenue —
// and dropping the vocabulary there would make the corpus worst at exactly the
// questions only the corpus can answer.
func (b *Brief) about(o Options) []corpus.Metric {
	if o.Engine == nil || o.Engine.Model == nil {
		return nil
	}
	names := b.Metrics
	if len(names) == 0 {
		names = narrowed(b.candidates, o.Engine.Model.MetricNames())
	}
	out := make([]corpus.Metric, 0, len(names))
	for _, name := range names {
		m := o.Engine.Model.Metric(name)
		if m == nil {
			continue
		}
		out = append(out, corpus.Metric{Name: m.Name, Description: m.Description, Synonyms: m.Synonyms})
	}
	return out
}

// narrowed separates a clarification that is about the question from one that
// is only a menu.
//
// The grounder answers "which measure did you mean" two ways. When a question
// is genuinely ambiguous it offers the few metrics that could fit, and those
// are what the question is about. When nothing matched at all it offers the
// whole catalogue, and that is a menu — it says nothing about the question.
//
// Feeding a menu to the corpus as vocabulary brings back the same wrong
// citation the relevance floor exists to prevent: asking about forklift
// maintenance on a one-metric model offered `revenue`, and the revenue memo
// came back as evidence. A candidate list that is the entire model is
// therefore no list at all.
func narrowed(candidates, all []string) []string {
	if len(candidates) == 0 || len(candidates) >= len(all) {
		return nil
	}
	return candidates
}

// numbers runs the governed path and records why it produced nothing when it
// produces nothing. A question the model cannot ground is the ordinary case for
// anything about policy or history, and saying so plainly is more useful than
// an empty table.
func (b *Brief) numbers(ctx context.Context, question string, o Options) {
	q, _, clarify, err := o.Grounder.Ground(ctx, question)
	switch {
	case err != nil:
		b.NumbersBy = err.Error()
		return
	case clarify != nil:
		// Grounding already wrote the sentence that explains itself — "Which
		// measure? Name it and I will answer that one", or "No matching
		// metric". Relabelling both as "the question is ambiguous" and printing
		// only the candidate list produced "the question is ambiguous: revenue"
		// for a question about forklift maintenance: one candidate is not an
		// ambiguity, and the model's single metric is not a reading of the
		// question. Quote the message instead of inventing one.
		b.NumbersBy = strings.TrimSpace(clarify.Question)
		if b.NumbersBy == "" {
			b.NumbersBy = "the model could not tell which measure this asks for"
		}
		// The candidate list is a menu, and a menu of everything is not one.
		if n := len(clarify.Candidates); n > 0 && n <= 8 {
			b.NumbersBy += " (" + strings.Join(clarify.Candidates, ", ") + ")"
			b.candidates = clarify.Candidates
		}
		return
	case len(q.Metrics) == 0:
		b.NumbersBy = "no metric in the model matches this question"
		return
	}
	b.Query, b.Metrics = q, q.Metrics

	ans, err := governance.Query(ctx, o.Engine, q, o.Who, o.Policy)
	if err != nil {
		b.NumbersBy = err.Error()
		return
	}
	b.Columns, b.Rows, b.SQL = ans.Columns, ans.Rows, ans.SQL
	b.attest(ctx, o)
}

// attest asks the registry who approved the model that just answered.
func (b *Brief) attest(ctx context.Context, o Options) {
	b.ModelHash = o.Engine.ModelHash
	if o.Registry == nil || b.ModelHash == "" {
		return
	}
	a, found, err := o.Registry.Signature(ctx, b.ModelHash)
	if err != nil {
		return
	}
	b.Attested = found
	b.SignedBy, b.SignedAt, b.SignNote, b.Promoted = a.SignedBy, a.SignedAt, a.Note, a.Promoted
}

// Text renders a brief for a terminal. The order is deliberate: the number
// first because it is what was asked for, then who stands behind the definition
// of it, then what was written about it. A reader who stops after the first
// line has the answer; a reader who does not stop has the argument.
func (b *Brief) Text() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Q: %s\n\n", b.Question)

	if len(b.Rows) > 0 {
		fmt.Fprintf(&sb, "%s\n", strings.Join(b.Columns, " | "))
		for _, row := range b.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = cell(c)
			}
			fmt.Fprintf(&sb, "%s\n", strings.Join(cells, " | "))
		}
	} else if b.NumbersBy != "" {
		fmt.Fprintf(&sb, "no figure: %s\n", b.NumbersBy)
	}

	if b.ModelHash != "" {
		switch {
		case b.SignedBy != "" && b.Promoted:
			fmt.Fprintf(&sb, "\ndefinition %s — approved by %s on %s", b.ModelHash, b.SignedBy, b.SignedAt)
			if b.SignNote != "" {
				fmt.Fprintf(&sb, " (%s)", b.SignNote)
			}
			sb.WriteString("\n")
		case b.SignedBy != "":
			fmt.Fprintf(&sb, "\ndefinition %s — signed by %s but never promoted through the registry\n",
				b.ModelHash, b.SignedBy)
		default:
			fmt.Fprintf(&sb, "\ndefinition %s — NOBODY has approved this model\n", b.ModelHash)
		}
	}

	if len(b.Passages) > 0 {
		sb.WriteString("\nwritten about it:\n")
		for _, p := range b.Passages {
			tag := ""
			if p.Tagged {
				// Say which citations are assertions rather than matches. A
				// reader weighs "somebody filed this under 一次合格率_铸造"
				// differently from "these words appeared together".
				tag = " · filed under " + p.Metric
			}
			fmt.Fprintf(&sb, "  [%s%s] %s\n", p.DocumentID, tag, oneLine(p.Text, 240))
		}
	}
	if len(b.Sources()) == 0 {
		sb.WriteString("\nneither the warehouse nor the corpus had anything for this question.\n")
	}
	return sb.String()
}

// cell renders one value for a person.
//
// A ratio came back from Postgres as 0.97094126231302444363 and was printed
// that way. Twenty digits is not more accurate than six, it is less readable
// than six, and nobody reading a first-pass yield rate is helped by the
// twentieth decimal of a number whose inputs are counts of castings.
//
// Only the rendering is rounded. The structured payload a program reads keeps
// whatever the warehouse sent, because a caller reconciling against another
// system needs the exact value and a caller reading a report does not.
func cell(v any) string {
	s := fmt.Sprint(v)
	switch t := v.(type) {
	case float64:
		return trimFloat(strconv.FormatFloat(t, 'f', 6, 64))
	case float32:
		return trimFloat(strconv.FormatFloat(float64(t), 'f', 6, 64))
	case []byte:
		s = string(t)
	case string:
		s = t
	default:
		return s
	}
	// Postgres NUMERIC arrives as text, and so does anything a driver does not
	// map. Round it only if it really is a number with more precision than a
	// reader wants; leave every other string exactly as it came.
	if f, err := strconv.ParseFloat(s, 64); err == nil && strings.Contains(s, ".") {
		if i := strings.Index(s, "."); len(s)-i-1 > 6 {
			return trimFloat(strconv.FormatFloat(f, 'f', 6, 64))
		}
	}
	return s
}

// trimFloat drops the trailing zeros a fixed-precision format leaves behind, so
// 122 does not print as 122.000000.
func trimFloat(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary so a multi-byte character is never split in half.
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
