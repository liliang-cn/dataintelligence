package mcp

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/dataintelligence/board"
	"github.com/liliang-cn/dataintelligence/governance"
)

// The dashboard an agent proposes and does not write.
//
// This is the tool the board package was built for, and until now there was no
// way to reach it: `di board` could make one from the command line and nothing
// could make one on behalf of a model. An agent asked for a dashboard had two
// options, and both were bad — describe one in prose, or call query_metric a
// dozen times and assemble the numbers itself. The second is the dangerous one:
// a model that assembles a dashboard is a model that can assemble the
// dashboard proving its own point, and a dashboard is precisely the artefact
// people stop checking.
//
// The split is the whole design. The model contributes the layout — which
// metrics, broken down by which dimensions — and every number, every chart and
// every refusal is produced here, compiled from the semantic model and executed
// through governance as the real caller. A panel naming something the model
// cannot resolve is refused by name rather than quietly dropped, because a
// board that silently loses a panel is a board that answers a different
// question from the one that was asked.
//
// Calling it with no panels asks for the proposal, which reads the trail: a
// board of what this deployment's own people have asked for. That is the
// useful default for an agent that was told "show me a dashboard" and has no
// opinion about what belongs on it.

type panelIn struct {
	Title   string   `json:"title,omitempty" jsonschema:"panel heading; defaults to a description of its contents"`
	Metrics []string `json:"metrics" jsonschema:"metric names from the semantic model — call list_metrics first"`
	GroupBy []string `json:"group_by,omitempty" jsonschema:"dimension names to break down by — call get_dimensions first"`
	Grain   string   `json:"grain,omitempty" jsonschema:"time grain: day|week|month|quarter|year"`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum rows in this panel"`
	Chart   string   `json:"chart,omitempty" jsonschema:"bar|line|pie|none; omit to let the host choose from the shape of the data"`
}

type boardIn struct {
	Title  string    `json:"title,omitempty" jsonschema:"board heading"`
	Panels []panelIn `json:"panels,omitempty" jsonschema:"the layout you propose; omit to get a board proposed from what people have asked for"`
}

func (s *srv) board(ctx context.Context, req *mcpsdk.CallToolRequest, in boardIn) (*mcpsdk.CallToolResult, any, error) {
	p, deny := s.guard(req, "metrics:read")
	if deny != nil {
		return deny, nil, nil
	}
	if s.eng == nil || !s.eng.Governed() {
		return errResult("a board needs a semantic model; this database has none"), nil, nil
	}

	panels := make([]board.Panel, 0, len(in.Panels))
	for _, pi := range in.Panels {
		if len(pi.Metrics) == 0 {
			return errResult("every panel needs at least one metric — call list_metrics to see them"), nil, nil
		}
		panels = append(panels, board.Panel{
			Title: pi.Title, Metrics: pi.Metrics, GroupBy: pi.GroupBy,
			Grain: pi.Grain, Limit: pi.Limit, Chart: pi.Chart,
		})
	}
	if len(panels) == 0 {
		panels = board.ProposeFor(ctx, s.eng, s.eng.Model)
		if len(panels) == 0 {
			return errResult("nothing to show: the model declares no metrics"), nil, nil
		}
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		if title = s.eng.Model.Name; title == "" {
			title = "Board"
		}
	}
	b, err := board.Build(ctx, s.eng,
		governance.Principal{User: p.User, Role: p.Role, Question: title},
		governance.DefaultPolicy(), title, panels)
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	fence, err := b.Fence()
	if err != nil {
		return errResult(err.Error()), nil, nil
	}

	// The provenance rides along, for the same reason brief carries it: a
	// dashboard is worth arguing about, and the first question is whose
	// definitions it used.
	var hash, by, at, note string
	hash = s.eng.ModelHash
	if s.opts.Registry != nil && hash != "" {
		if a, _, err := s.opts.Registry.Signature(ctx, hash); err == nil {
			by, at, note = a.SignedBy, a.SignedAt, a.Note
		}
	}
	// The layout, packed for a URL. An agent that assembled a board can hand
	// the person a link to it — `/ui/board?layout=…` on this deployment's
	// console — instead of pasting JSON at them. The link carries no numbers
	// and no identity: whoever opens it gets the board computed for them.
	layout, _ := board.EncodeLayout(panels)
	out := map[string]any{
		"panels":    len(b.Panels),
		"refused":   b.Refused(),
		"failed":    b.Failed(),
		"layout":    layout,
		"open_at":   "/ui/board?layout=" + layout,
		"model":     hash,
		"signed_by": by,
		"signed_at": at,
		"reason":    note,
	}
	return textResult(fence), out, nil
}

// boardDescription tells a model what it is and is not being asked to do.
const boardDescription = "Render a BI dashboard as an AIGUI ```dashboard fence. " +
	"You propose the layout — which metrics, broken down by which dimensions — and this computes every " +
	"number, chart and refusal from the semantic model, as the calling user. " +
	"Do NOT assemble a dashboard yourself out of query_metric results: numbers you write are numbers nobody checked. " +
	"Omit `panels` to get a board proposed from the metrics this deployment's people actually ask for. " +
	"The result carries `open_at`: a link that renders this same board in the console for whoever opens it."
