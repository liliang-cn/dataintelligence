// Package platform is the whole product in one object.
//
// Five libraries, none of which knows about the others, assembled here because
// a delivery needs all five and an engineer standing one up should not have to
// know which is which:
//
//	semantic-go   the口径: metrics, dimensions, a declared join graph and grain,
//	              compiled to fan-out- and chasm-safe SQL
//	cortexdb      the brain: the graph, the vectors, the retrieval, the
//	              bitemporal history — and nothing above it
//	alchemy       the pipeline: documents in, a vocabulary and a graph out,
//	              with a review queue for what it is unsure of
//	athanor       four workflows over the two below it: rules, snapshots,
//	              ontologies, and a live database's signed privacy plan
//	agent-go      the harness: tool loops and MCP, for the conversational half
//
// This repository adds the parts that are about the delivery rather than about
// any one layer: the registry that makes a口径 signable, the corpus that holds
// what was written about it, the brief that answers with both, and the graph
// projection that makes the answer explainable.
//
// # Why one object rather than a package per layer
//
// Every one of these opens something — a warehouse connection, a brain file, an
// index — and an engineer who wires them by hand gets a subset: the graph
// without the registry, so nothing says who approved the definition it was
// built from; the corpus in one file and the graph in another, so a passage can
// never be walked to the metric it explains. Each of those is a working
// deployment that quietly answers less than it should, which is the failure
// mode this product exists to refuse.
//
// So the assembly is one function with one rule: anything that cannot be
// opened is reported and left nil, and the parts that remain still work. A
// deployment with no documents still signs口径. A deployment with no warehouse
// still explains a graph. What it does not do is pretend.
package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/athanor/pkg/ontologies"
	"github.com/liliang-cn/athanor/pkg/rules"
	"github.com/liliang-cn/athanor/pkg/snapshots"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/rollout"
)

// Config is what an engineer has on day one at a customer: a database, a model
// file, and somewhere to keep what gets written down.
type Config struct {
	DSN       string // the customer's warehouse
	ModelPath string // the semantic model; empty means unmodelled
	BrainPath string // where the corpus, the graph and the ontologies live
	IndexPath string // the metric index; empty means a temp file

	// Embedder is shared by the corpus and the graph. Nil falls back to
	// lexical retrieval rather than failing, because a first visit rarely has
	// an embedding endpoint approved yet.
	Embedder corpus.Embedder
}

// Platform is everything, assembled.
//
// Every field may be nil. The Missing list says which and why, in the words an
// engineer would use to fix it, and it is the first thing `di status` prints.
type Platform struct {
	Engine    *engine.Engine      // OLAP: compile and run a semantic query
	Grounder  *grounding.Grounder // questions in words
	Registry  *rollout.Registry   // who approved which口径
	Corpus    *corpus.Store       // what was written about it
	Brain     *cortexdb.DB        // the graph and the store under it
	Rules     *rules.Store        // derivation rules, with a ledger
	Snapshots *snapshots.Store    // named moments of the brain
	Ontology  *ontologies.Store   // the vocabulary documents load under

	ModelHash string
	Missing   []string

	closers []func()
}

// Open assembles the platform, reporting what it could not reach.
func Open(ctx context.Context, cfg Config) (*Platform, error) {
	p := &Platform{}
	miss := func(what, why string) { p.Missing = append(p.Missing, what+": "+why) }

	if cfg.DSN != "" {
		eng, err := engine.New(ctx, cfg.ModelPath, cfg.DSN)
		switch {
		case err != nil:
			miss("warehouse", err.Error())
		default:
			p.Engine, p.ModelHash = eng, eng.ModelHash
			p.closers = append(p.closers, func() { _ = eng.Close() })
			// The registry rides on the engine's own handle: the ledger it
			// reads was written through that same warehouse.
			p.Registry = rollout.New(eng.WH, nowUTC)
		}
	} else {
		miss("warehouse", "no DSN — set DI_DSN or pass -dsn")
	}

	if p.Engine != nil && p.Engine.Governed() {
		path := cfg.IndexPath
		if path == "" && cfg.BrainPath != "" {
			path = cfg.BrainPath + ".index"
		}
		if path == "" {
			// Neither was given. The metric index is derived — it is rebuilt
			// from the model on every open — so a temp file is the right home
			// for it, and `cfg.BrainPath + ".index"` with an empty BrainPath
			// is the wrong one: it resolves to `.index` in the working
			// directory, which during a test is the source tree. A package
			// that writes into its own directory is one whose test run shows
			// up in `git status`.
			d, err := os.MkdirTemp("", "di-index-")
			if err != nil {
				miss("grounding", err.Error())
				return p, nil
			}
			path = filepath.Join(d, "idx.db")
			p.closers = append(p.closers, func() { _ = os.RemoveAll(d) })
		}
		if g, err := grounding.New(ctx, p.Engine.Model, path); err != nil {
			miss("grounding", err.Error())
		} else {
			p.Grounder = g
			p.closers = append(p.closers, func() { _ = g.Close() })
		}
	} else if p.Engine != nil {
		miss("grounding", "this database has no semantic model, so there are no metrics to ground on")
	}

	if cfg.BrainPath != "" {
		emb := cfg.Embedder
		if emb == nil {
			emb = corpus.LexicalEmbedder{}
		}
		bc := cortexdb.DefaultConfig(cfg.BrainPath)
		bc.Dimensions = emb.Dim()
		db, err := cortexdb.Open(bc, cortexdb.WithEmbedder(emb))
		if err != nil {
			miss("brain", err.Error())
		} else {
			p.Brain = db
			p.closers = append(p.closers, func() { _ = db.Close() })
			p.Corpus = corpus.Wrap(db)
			// The three athanor workflows all sit on the same brain, which is
			// what lets a rule's conclusion, a snapshot's contents and a
			// document's vocabulary refer to one another.
			if s, err := rules.New(db); err != nil {
				miss("rules", err.Error())
			} else {
				p.Rules = s
			}
			if s, err := snapshots.New(db); err != nil {
				miss("snapshots", err.Error())
			} else {
				p.Snapshots = s
			}
			if s, err := ontologies.New(db); err != nil {
				miss("ontologies", err.Error())
			} else {
				p.Ontology = s
			}
		}
	} else {
		miss("brain", "no brain path — set DI_BRAIN")
	}

	return p, nil
}

// Close releases everything Open opened, in reverse.
func (p *Platform) Close() {
	for i := len(p.closers) - 1; i >= 0; i-- {
		p.closers[i]()
	}
}

// Model is the loaded semantic model, or nil.
func (p *Platform) Model() *semantic.Model {
	if p.Engine == nil {
		return nil
	}
	return p.Engine.Model
}

// Status is what `di status` prints: which halves are live, and for the ones
// that are not, the sentence that says how to fix it.
func (p *Platform) Status() string {
	var b strings.Builder
	line := func(name string, ok bool, detail string) {
		mark := "  —"
		if ok {
			mark = "  ok"
		}
		fmt.Fprintf(&b, "%-12s%s  %s\n", name, mark, detail)
	}
	model := "unmodelled"
	if m := p.Model(); m != nil {
		model = fmt.Sprintf("%d metrics, %d dimensions, %d entities · %s",
			len(m.Metrics), len(m.Dimensions), len(m.Entities), p.ModelHash)
	}
	line("warehouse", p.Engine != nil, model)
	line("questions", p.Grounder != nil, modeOf(p.Grounder))
	line("registry", p.Registry != nil, "口径签字与账本")
	line("corpus", p.Corpus != nil, "写下来的东西")
	line("graph", p.Brain != nil, "可解释的口径结构")
	line("rules", p.Rules != nil, "推导规则")
	line("snapshots", p.Snapshots != nil, "具名时刻")
	line("ontology", p.Ontology != nil, "装载用的词表")
	for _, m := range p.Missing {
		fmt.Fprintf(&b, "\n-- %s", m)
	}
	if len(p.Missing) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

func modeOf(g *grounding.Grounder) string {
	if g == nil {
		return ""
	}
	return g.Mode()
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
