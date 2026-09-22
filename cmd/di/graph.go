package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/modelgraph"
	"github.com/liliang-cn/dataintelligence/rollout"
)

// defaultBrain is where the graph and the corpus share one store. They are the
// same deployment's knowledge — the documents somebody wrote and the structure
// of the definitions they wrote about — and splitting them across two files
// would mean two backups, two paths to get wrong, and no way to walk from a
// passage to the metric it explains.
func defaultBrain() string {
	if p := os.Getenv("DI_BRAIN"); p != "" {
		return p
	}
	return defaultCorpus()
}

// runGraph is the explainable half of the product.
//
//	di graph build   -model models/axle.yaml
//	di graph explain 一次合格率_统一口径
//	di graph impact  pouring.poured_qty
//
// The graph is projected from the signed semantic model, not extracted from
// text, so every edge in it traces to a line somebody wrote and approved. See
// modelgraph's package comment for why that distinction is the whole point.
func runGraph(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di graph <build|explain|impact> [flags]")
		os.Exit(2)
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("graph "+sub, flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	brain := fs.String("brain", defaultBrain(), "brain database file")
	depth := fs.Int("depth", 4, "how many definitions deep to walk")
	_ = fs.Parse(sortFlagsFirst(fs, rest))

	ctx := context.Background()
	db, err := openBrain(*brain)
	if err != nil {
		fail(err)
	}
	defer db.Close()

	switch sub {
	case "build":
		b, err := os.ReadFile(*model)
		if err != nil {
			fail(err)
		}
		m, err := semantic.Load(b)
		if err != nil {
			fail(err)
		}
		hash := rollout.HashBytes(b)
		p, err := modelgraph.Build(ctx, db.Graph(), m, hash, db.Info().Dimensions)
		if err != nil {
			fail(err)
		}
		fmt.Printf("model %s → %d nodes, %d edges\n", p.ModelHash, p.Nodes, p.Edges)
		// Whether anybody approved this model is the same question the brief
		// asks, and the answer belongs here too: a graph built from an
		// unapproved definition is a graph of somebody's draft.
		if reg, err := registryFor(ctx, fs); err == nil && reg != nil {
			if a, found, err := reg.Signature(ctx, hash); err == nil && found && a.Promoted {
				fmt.Printf("this definition was approved by %s on %s\n", a.SignedBy, a.SignedAt)
			} else if err == nil {
				fmt.Println("NOBODY has approved this model — the graph is a draft's shape")
			}
		}
	case "explain":
		name := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if name == "" {
			fail(fmt.Errorf("graph explain needs a metric name"))
		}
		rows, err := modelgraph.Explain(ctx, db.Graph(), name, *depth)
		if err != nil {
			fail(err)
		}
		if len(rows) == 0 {
			fmt.Printf("%s rests on nothing — it is a base metric with no columns the model names\n", name)
			return
		}
		fmt.Printf("%s rests on:\n", name)
		for _, r := range rows {
			fmt.Printf("  %s%-9s %s\n", strings.Repeat("  ", r.Depth-1), r.Kind, r.Name)
		}
	case "impact":
		col := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if col == "" {
			fail(fmt.Errorf("graph impact needs a column, as table.column"))
		}
		rows, err := modelgraph.Impact(ctx, db.Graph(), col, *depth)
		if err != nil {
			fail(err)
		}
		if len(rows) == 0 {
			fmt.Printf("nothing in the model reads %s\n", col)
			return
		}
		fmt.Printf("changing %s moves %d metric(s):\n", col, len(rows))
		for _, r := range rows {
			note := ""
			if r.Hops > 1 {
				note = fmt.Sprintf("  (%d definitions away — the ones nobody remembers)", r.Hops)
			}
			fmt.Printf("  %s%s\n", r.Metric, note)
		}
	default:
		fail(fmt.Errorf("unknown graph subcommand %q", sub))
	}
}

// openBrain opens the shared store the corpus and the graph both live in.
func openBrain(path string) (*cortexdb.DB, error) {
	emb := corpusEmbedder()
	if emb == nil {
		emb = corpus.LexicalEmbedder{}
	}
	cfg := cortexdb.DefaultConfig(path)
	cfg.Dimensions = emb.Dim()
	return cortexdb.Open(cfg, cortexdb.WithEmbedder(emb))
}

// registryFor opens the model registry against the warehouse, when one is
// reachable. A graph build with no warehouse in sight is a normal thing to do
// while modelling, so this reports nothing rather than failing.
func registryFor(ctx context.Context, fs *flag.FlagSet) (*rollout.Registry, error) {
	dsn := envOr("DI_DSN", "")
	if dsn == "" {
		return nil, nil
	}
	wh, err := openWarehouse(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return rollout.New(wh, nowUTC), nil
}
