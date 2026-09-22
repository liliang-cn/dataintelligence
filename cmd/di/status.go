package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/liliang-cn/dataintelligence/platform"
)

// runStatus is the first command to run at a customer: what is wired, what is
// not, and what to type to fix the parts that are not.
//
//	di status
//
// It exists because the platform degrades rather than refuses — a deployment
// with no corpus still signs口径, a deployment with no warehouse still explains
// a graph — and a product that quietly answers less than it could is one
// nobody can tell is half-configured.
func runStatus(argv []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	model := fs.String("model", envOr("DI_MODEL", "models/meridian.yaml"), "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", ""), "warehouse DSN")
	brain := fs.String("brain", defaultBrain(), "brain database file")
	_ = fs.Parse(sortFlagsFirst(fs, argv))

	p, err := platform.Open(context.Background(), platform.Config{
		DSN: *dsn, ModelPath: *model, BrainPath: *brain, Embedder: corpusEmbedder(),
	})
	if err != nil {
		fail(err)
	}
	defer p.Close()
	fmt.Print(p.Status())
	if len(p.Missing) > 0 {
		os.Exit(1)
	}
}
