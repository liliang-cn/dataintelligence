package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/platform"
	"github.com/liliang-cn/dataintelligence/reported"
)

// runReported freezes a figure that was sent, and later says why today's
// figure is different.
//
//	di reported freeze -name 2026Q2-吨件电耗 -metrics 吨件电耗 -by workshop_name -sender 王工
//	di reported compare 2026Q2-吨件电耗
//	di reported list
//
// The question this exists for is the one asked months after a report went
// out: "you told us 1053, why does it say 1132 now?" There are three answers —
// the definition was re-signed, the access rules changed, or late data
// arrived — and they must never look alike, because only one of them is
// somebody's decision.
func runReported(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di reported <freeze|compare|list> [flags]")
		os.Exit(2)
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("reported "+sub, flag.ExitOnError)
	model := fs.String("model", envOr("DI_MODEL", "models/meridian.yaml"), "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	brain := fs.String("brain", defaultBrain(), "brain database file")
	name := fs.String("name", "", "the name the report will be quoted back by (freeze)")
	metrics := fs.String("metrics", "", "comma-separated metrics (freeze)")
	by := fs.String("by", "", "comma-separated dimensions to break down by (freeze)")
	grain := fs.String("grain", "", "time grain (freeze)")
	role := fs.String("role", "analyst", "the role the figure is computed as")
	sender := fs.String("sender", "", "who sent it (freeze)")
	note := fs.String("note", "", "what was said with it; for a correction, why")
	supersedes := fs.String("supersedes", "", "the report this one corrects (freeze)")
	_ = fs.Parse(sortFlagsFirst(fs, rest))

	ctx := context.Background()
	p, err := platform.Open(ctx, platform.Config{
		DSN: *dsn, ModelPath: *model, BrainPath: *brain, Embedder: corpusEmbedder(),
	})
	if err != nil {
		fail(err)
	}
	defer p.Close()
	if p.Reported == nil {
		fail(fmt.Errorf("reports are kept in the brain, which could not be opened: %s", strings.Join(p.Missing, "; ")))
	}
	pol := governance.DefaultPolicy()

	switch sub {
	case "freeze":
		if p.Engine == nil || !p.Engine.Governed() {
			fail(fmt.Errorf("freezing a figure needs a warehouse and a model: %s", strings.Join(p.Missing, "; ")))
		}
		if *metrics == "" {
			fail(fmt.Errorf("freeze needs -metrics"))
		}
		q := semantic.Query{Metrics: splitList(*metrics), GroupBy: splitList(*by), TimeGrain: *grain}
		who := governance.Principal{User: *sender, Role: *role, Question: *name}
		ans, err := governance.Query(ctx, p.Engine, q, who, pol)
		if err != nil {
			fail(err)
		}
		// The model file's bytes travel with the report. Without them a later
		// comparison can say the definition changed, but not how much of the
		// change was the definition and how much was the data.
		modelBytes, err := os.ReadFile(*model)
		if err != nil {
			fail(err)
		}
		f := reported.FromAnswer(p.Engine, q, ans)
		f.Name, f.By, f.Note, f.Supersedes = *name, *sender, *note, *supersedes
		f.Who, f.Policy, f.Model = who, pol, modelBytes
		r, err := p.Reported.Freeze(ctx, f)
		if err != nil {
			fail(err)
		}
		fmt.Printf("froze %s: %d row(s) from definition %s, sent by %s at %s\n",
			r.Name, len(r.Rows), r.ModelHash, r.By, r.At.Format("2006-01-02 15:04"))
	case "compare":
		n := *name
		if n == "" && fs.NArg() > 0 {
			n = fs.Arg(0)
		}
		if n == "" {
			fail(fmt.Errorf("compare needs a report name"))
		}
		if p.Engine == nil {
			fail(fmt.Errorf("comparing needs today's warehouse: %s", strings.Join(p.Missing, "; ")))
		}
		c, err := p.Reported.Compare(ctx, n, reported.Current{Engine: p.Engine, Policy: pol})
		if errors.Is(err, reported.ErrNotFound) {
			fail(fmt.Errorf("no report called %q — `di reported list` shows the ones that exist", n))
		}
		if err != nil {
			fail(err)
		}
		fmt.Print(c.Text())
	case "list":
		rs, err := p.Reported.List(ctx)
		if err != nil {
			fail(err)
		}
		if len(rs) == 0 {
			fmt.Println("-- nothing has been frozen yet")
			return
		}
		for _, r := range rs {
			line := fmt.Sprintf("%-28s %s  by %-8s %s  %v", r.Name, r.At.Format("2006-01-02 15:04"), r.By, r.ModelHash, r.Query.Metrics)
			if r.SupersededBy != "" {
				line += "  (corrected by " + r.SupersededBy + ")"
			}
			fmt.Println(line)
		}
	default:
		fail(fmt.Errorf("unknown reported subcommand %q", sub))
	}
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
