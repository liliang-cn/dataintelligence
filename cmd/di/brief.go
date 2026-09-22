package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/brief"
	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// defaultCorpus is where the document half lives unless told otherwise. It sits
// beside the model rather than in a temp directory because, unlike the metric
// index, it is not derived from anything: losing it loses the documents.
func defaultCorpus() string {
	if p := os.Getenv("DI_CORPUS"); p != "" {
		return p
	}
	return "corpus.db"
}

// sortFlagsFirst moves flags ahead of positional arguments.
//
// Go's flag package stops parsing at the first non-flag word, so
// `di corpus add memo.md -kind definition` silently treats `-kind` and
// `definition` as two more files to ingest — which is what happened the first
// time this command was run, and the error it produced named a missing file
// rather than the real problem. Every other CLI a person uses accepts flags
// after arguments, so the fix is to accept them too rather than to document a
// rule nobody will read.
//
// A flag that takes a value must keep it, and `flag` allows both `-k 4` and
// `-k=4`, so a bare `-name value` pair has to travel together. The set knows
// which flags are booleans; anything else is assumed to consume the next word.
func sortFlagsFirst(fs *flag.FlagSet, argv []string) []string {
	isBool := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			isBool[f.Name] = true
		}
	})
	var flags, rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			rest = append(rest, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if n, _, ok := strings.Cut(name, "="); ok {
			name = n
			continue // the value travelled with it
		}
		if !isBool[name] && i+1 < len(argv) {
			i++
			flags = append(flags, argv[i])
		}
	}
	return append(flags, rest...)
}

// runCorpus manages the document half.
//
//	di corpus add memo.md -kind definition -metric revenue
//	di corpus add docs/            # every .md under a directory
//	di corpus search "why do we exclude refunds"
func runCorpus(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di corpus <add|search> [flags]")
		os.Exit(2)
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("corpus "+sub, flag.ExitOnError)
	path := fs.String("corpus", defaultCorpus(), "corpus database file")
	kind := fs.String("kind", "", "what sort of document this is (definition|dictionary|ticket|runbook|…)")
	metric := fs.String("metric", "", "the metric this document is about, if it is about one")
	k := fs.Int("k", 4, "how many passages to return (search)")
	_ = fs.Parse(sortFlagsFirst(fs, rest))

	ctx := context.Background()
	store, err := corpus.Open(*path, corpusEmbedder())
	if err != nil {
		fail(err)
	}
	defer store.Close()

	switch sub {
	case "add":
		if fs.NArg() == 0 {
			fail(fmt.Errorf("corpus add needs a file or directory"))
		}
		total, docs := 0, 0
		for _, arg := range fs.Args() {
			for _, f := range expandDocs(arg) {
				n, err := store.AddFile(ctx, f, *kind, *metric)
				if err != nil {
					fmt.Fprintf(os.Stderr, "-- %s: %v\n", f, err)
					continue
				}
				fmt.Printf("%-40s %3d chunks\n", filepath.Base(f), n)
				total += n
				docs++
			}
		}
		fmt.Printf("%d document(s), %d chunk(s) → %s\n", docs, total, *path)
	case "search":
		q := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if q == "" {
			fail(fmt.Errorf("corpus search needs a question"))
		}
		hits, err := store.Recall(ctx, q, *k)
		if err != nil {
			fail(err)
		}
		if len(hits) == 0 {
			fmt.Println("-- nothing in the corpus bears on that")
			return
		}
		for _, h := range hits {
			fmt.Printf("[%s]\n%s\n\n", h.DocumentID, strings.TrimSpace(h.Text))
		}
	default:
		fail(fmt.Errorf("unknown corpus subcommand %q", sub))
	}
}

// expandDocs turns a path into the documents under it. A directory contributes
// its markdown and text files; a file contributes itself whatever it is called,
// because somebody who names a file explicitly means it.
func expandDocs(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-- %s: %v\n", path, err)
		return nil
	}
	if !info.IsDir() {
		return []string{path}
	}
	var out []string
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".md", ".markdown", ".txt":
			out = append(out, p)
		}
		return nil
	})
	return out
}

// corpusEmbedder wires the embedding endpoint when one is configured. Without
// it the corpus retrieves lexically, which is a weaker configuration and not a
// broken one — so it is a fallback rather than a failure.
func corpusEmbedder() corpus.Embedder {
	e, err := grounding.EmbedderFromEnv()
	if err != nil || e == nil {
		return nil
	}
	return corpus.Adapt(e)
}

// runBrief answers one question from both halves at once.
//
//	di brief "net revenue this quarter"
//
// It is the only command here that a warehouse alone cannot serve: the figure
// comes from the warehouse, the definition's approver from the registry, and
// the argument behind the definition from the corpus.
func runBrief(argv []string) {
	fs := flag.NewFlagSet("brief", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	corpusPath := fs.String("corpus", defaultCorpus(), "corpus database file")
	role := fs.String("role", "analyst", "caller role (governance)")
	passages := fs.Int("k", 3, "how many passages to cite")
	_ = fs.Parse(sortFlagsFirst(fs, argv))

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `di brief: ask something, e.g. di brief "net revenue"`)
		os.Exit(2)
	}
	ctx := context.Background()

	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	dir, _ := os.MkdirTemp("", "di-brief-")
	defer os.RemoveAll(dir)
	g, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		fail(err)
	}
	defer g.Close()

	opts := brief.Options{
		Engine: eng, Grounder: g, Passages: *passages,
		Who:    governance.Principal{User: "cli", Role: *role},
		Policy: governance.DefaultPolicy(),
	}
	// Both of these are optional. A deployment with no corpus, or one whose
	// registry table has never been created, still gets the half it has.
	if store, err := corpus.Open(*corpusPath, corpusEmbedder()); err == nil {
		defer store.Close()
		opts.Corpus = store
	} else {
		fmt.Fprintf(os.Stderr, "-- no corpus at %s: %v\n", *corpusPath, err)
	}
	if regWH, err := warehouse.Open(ctx, *dsn, warehouse.Options{}); err == nil {
		defer regWH.Close()
		opts.Registry = rollout.New(regWH, func() string { return time.Now().UTC().Format(time.RFC3339) })
	}

	b, err := brief.Answer(ctx, question, opts)
	if err != nil {
		fail(err)
	}
	fmt.Print(b.Text())
}

// briefParts opens the two optional halves of a brief for an MCP server.
//
// Both are best-effort by design. A deployment that has never written a
// document, or whose registry table has never been created, must still serve
// the brief tool — what it returns there is a figure with the honest admission
// that nothing stands behind it, which is more useful than the tool being
// absent.
//
// The registry rides on the engine's own warehouse handle rather than opening a
// second connection to the same database: the ledger it reads was written
// through that same warehouse, and two handles to one SQLite file is a way to
// find out about locking at the worst moment.
func briefParts(eng *engine.Engine) (*corpus.Store, *rollout.Registry, func()) {
	var store *corpus.Store
	closer := func() {}
	if s, err := corpus.Open(defaultCorpus(), corpusEmbedder()); err == nil {
		store = s
		closer = func() { _ = s.Close() }
	} else {
		fmt.Fprintf(os.Stderr, "-- brief: no corpus at %s: %v\n", defaultCorpus(), err)
	}
	reg := rollout.New(eng.WH, func() string { return time.Now().UTC().Format(time.RFC3339) })
	return store, reg, closer
}

// nowUTC is the clock every ledger write in this binary shares.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// openWarehouse is warehouse.Open with this binary's defaults.
func openWarehouse(ctx context.Context, dsn string) (*warehouse.Warehouse, error) {
	return warehouse.Open(ctx, dsn, warehouse.Options{})
}
