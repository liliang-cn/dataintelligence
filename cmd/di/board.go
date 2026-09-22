package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/board"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/platform"
)

// runBoard writes a BI dashboard as an AIGUI ```dashboard fence.
//
//	di board                                   # proposed from the model
//	di board -panels panels.json               # a layout somebody wrote
//	di board -metrics gmv,net_revenue -by channel_name
//	di board -out board.md
//
// The layout may come from a person, a file, or the proposal below. The
// numbers never do: every panel is compiled and executed here, as the caller,
// through the governance layer — so a board shows exactly what its reader is
// allowed to see, and the panels they are not get a refusal in place of
// numbers rather than blanking the board.
func runBoard(argv []string) {
	fs := flag.NewFlagSet("board", flag.ExitOnError)
	model := fs.String("model", envOr("DI_MODEL", "models/meridian.yaml"), "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	brain := fs.String("brain", defaultBrain(), "brain database file")
	role := fs.String("role", "analyst", "caller role (governance)")
	title := fs.String("title", "", "board title (default: the model's name)")
	metrics := fs.String("metrics", "", "comma-separated metrics for a single panel")
	by := fs.String("by", "", "comma-separated dimensions to break down by")
	grain := fs.String("grain", "", "time grain (day|week|month|quarter|year)")
	chart := fs.String("chart", "", "bar|line|pie|none (default: chosen from the shape)")
	panelsFile := fs.String("panels", "", "JSON file describing the panels")
	out := fs.String("out", "", "write here (default: stdout)")
	fenceOnly := fs.Bool("fence", false, "print only the fence, without the provenance around it")
	_ = fs.Parse(sortFlagsFirst(fs, argv))

	ctx := context.Background()
	p, err := platform.Open(ctx, platform.Config{
		DSN: *dsn, ModelPath: *model, BrainPath: *brain, Embedder: corpusEmbedder(),
	})
	if err != nil {
		fail(err)
	}
	defer p.Close()
	if p.Engine == nil || !p.Engine.Governed() {
		fail(fmt.Errorf("a board needs a semantic model: %s", strings.Join(p.Missing, "; ")))
	}

	panels, err := panelsFor(*panelsFile, *metrics, *by, *grain, *chart, p.Model())
	if err != nil {
		fail(err)
	}
	name := *title
	if name == "" {
		name = p.Model().Name
		if name == "" {
			name = "Board"
		}
	}

	b, err := board.Build(ctx, p.Engine,
		governance.Principal{User: "cli", Role: *role}, governance.DefaultPolicy(),
		name, panels)
	if err != nil {
		fail(err)
	}

	var doc string
	if *fenceOnly {
		doc, err = b.Fence()
	} else {
		by, at, note := "", "", ""
		if p.Registry != nil {
			if a, found, err := p.Registry.Signature(ctx, p.ModelHash); err == nil && found && a.Promoted {
				by, at, note = a.SignedBy, a.SignedAt, a.Note
			}
		}
		doc, err = b.Markdown(p.ModelHash, by, at, note)
	}
	if err != nil {
		fail(err)
	}
	if *out == "" {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "-- %d panel(s) → %s\n", len(b.Panels), *out)
}

// panelsFor decides the layout: a file, a one-panel request, or a proposal.
func panelsFor(file, metrics, by, grain, chart string, m *semantic.Model) ([]board.Panel, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var panels []board.Panel
		if err := json.Unmarshal(raw, &panels); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if len(panels) == 0 {
			return nil, fmt.Errorf("%s describes no panels", file)
		}
		return panels, nil
	}
	if metrics != "" {
		return []board.Panel{{
			Title:   strings.Join(split(metrics), ", "),
			Metrics: split(metrics),
			GroupBy: split(by),
			Grain:   grain,
			Chart:   chart,
		}}, nil
	}
	return board.Propose(m), nil
}
