// Package mcp exposes the platform's foundational tools as a standalone MCP
// server (official github.com/modelcontextprotocol/go-sdk). Security (M17):
// per-request identity from the bearer token → governance principal, per-tool
// scope checks, and per-principal rate limits. query_metric runs through the
// governance layer, so RBAC/masking/audit apply to the *real caller* — no
// confused deputy, no shared service account.
package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/connectors"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/ingest"
)

type srv struct {
	eng  *engine.Engine
	opts *Options
	rl   *rateLimiter
}

// NewServer builds the MCP server with the four foundational tools, guarded by
// identity + scope + rate limit. Pass nil opts for local defaults.
func NewServer(eng *engine.Engine, opts *Options) *mcpsdk.Server {
	if opts == nil {
		opts = defaultOptions()
	}
	s := &srv{eng: eng, opts: opts, rl: newRateLimiter(opts.RPS, opts.Burst)}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "dataintelligence", Title: "DataIntelligence semantic warehouse", Version: "0.1.0",
	}, nil)

	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "list_metrics",
		Description: "List the metrics available in the semantic model, with descriptions and synonyms. Call this first."},
		s.listMetrics)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_dimensions",
		Description: "Return the dimensions a metric can be grouped by WITHOUT a fanout. Use before query_metric."},
		s.getDimensions)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "query_metric",
		Description: "Run a governed semantic query: compute metrics, optionally grouped by dimensions. You name metrics/dimensions; the layer compiles safe SQL. Never write SQL yourself."},
		s.queryMetric)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ingest_csv",
		Description: "Ingest a CSV into a warehouse table: infer mapping, run key/data checks, land rows."},
		s.ingestCSV)
	return server
}

// principal resolves the caller from the request's bearer token, else the default.
func (s *srv) principal(req *mcpsdk.CallToolRequest) Principal {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		return principalFromToken(req.Extra.TokenInfo, s.opts.Default)
	}
	return s.opts.Default
}

// guard resolves identity, checks the required scope and the rate limit.
func (s *srv) guard(req *mcpsdk.CallToolRequest, scope string) (Principal, *mcpsdk.CallToolResult) {
	p := s.principal(req)
	if !p.hasScope(scope) {
		return p, errResult(fmt.Sprintf("caller %q lacks required scope %q", p.User, scope))
	}
	if !s.rl.allow(p.User) {
		return p, errResult(fmt.Sprintf("rate limit exceeded for %q", p.User))
	}
	return p, nil
}

type listIn struct{}

func (s *srv) listMetrics(_ context.Context, req *mcpsdk.CallToolRequest, _ listIn) (*mcpsdk.CallToolResult, any, error) {
	if _, deny := s.guard(req, "metrics:read"); deny != nil {
		return deny, nil, nil
	}
	type info struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Synonyms    []string `json:"synonyms,omitempty"`
	}
	var out []info
	var lines []string
	for i := range s.eng.Model.Metrics {
		m := &s.eng.Model.Metrics[i]
		out = append(out, info{m.Name, m.Description, m.Synonyms})
		lines = append(lines, "- "+m.Name+": "+m.Description)
	}
	return textResult(strings.Join(lines, "\n")), out, nil
}

type dimIn struct {
	Metric string `json:"metric" jsonschema:"the metric name to get valid grouping dimensions for"`
}

func (s *srv) getDimensions(_ context.Context, req *mcpsdk.CallToolRequest, in dimIn) (*mcpsdk.CallToolResult, any, error) {
	if _, deny := s.guard(req, "metrics:read"); deny != nil {
		return deny, nil, nil
	}
	dims, err := s.eng.Model.DimensionsFor(in.Metric)
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	return textResult(strings.Join(dims, ", ")), dims, nil
}

type queryIn struct {
	Metrics []string `json:"metrics" jsonschema:"metric names to compute"`
	GroupBy []string `json:"group_by,omitempty" jsonschema:"dimension names to slice by"`
	Grain   string   `json:"grain,omitempty" jsonschema:"time grain: day|month|quarter|year"`
	Limit   int      `json:"limit,omitempty" jsonschema:"max rows"`
}

func (s *srv) queryMetric(ctx context.Context, req *mcpsdk.CallToolRequest, in queryIn) (*mcpsdk.CallToolResult, any, error) {
	p, deny := s.guard(req, "metrics:read")
	if deny != nil {
		return deny, nil, nil
	}
	// Identity propagation: the query runs AS the caller — RBAC/masking/audit apply.
	ans, err := governance.Query(ctx, s.eng,
		semantic.Query{Metrics: in.Metrics, GroupBy: in.GroupBy, TimeGrain: in.Grain, Limit: in.Limit},
		governance.Principal{User: p.User, Role: p.Role}, governance.DefaultPolicy())
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	out := map[string]any{"columns": ans.Columns, "rows": ans.Rows, "sql": ans.SQL}
	return textResult(renderTable(ans.Columns, ans.Rows)), out, nil
}

type ingestIn struct {
	CSVPath  string   `json:"csv_path" jsonschema:"path to the CSV file"`
	Table    string   `json:"table" jsonschema:"target table name"`
	Fields   []string `json:"fields,omitempty" jsonschema:"target columns (default: cleaned CSV headers)"`
	Required []string `json:"required,omitempty" jsonschema:"target columns that must be non-empty"`
}

func (s *srv) ingestCSV(ctx context.Context, req *mcpsdk.CallToolRequest, in ingestIn) (*mcpsdk.CallToolResult, any, error) {
	if _, deny := s.guard(req, "data:write"); deny != nil { // destructive → write scope
		return deny, nil, nil
	}
	src := &connectors.CSVSource{Path: in.CSVPath}
	batch, err := src.Read(ctx)
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	plan := ingest.InferMapping(batch.Schema, in.Table, in.Fields)
	plan.Required = in.Required
	rep, err := ingest.Run(ctx, s.eng.WH, batch, plan)
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	return textResult(fmt.Sprintf("ingested into %q: read=%d landed=%d skipped=%d",
		rep.Table, rep.RowsRead, rep.RowsLanded, rep.RowsSkipped)), rep, nil
}

func textResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}
func errResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}
func renderTable(cols []string, rows [][]any) string {
	var b strings.Builder
	b.WriteString(strings.Join(cols, " | "))
	b.WriteByte('\n')
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = fmt.Sprintf("%v", c)
		}
		b.WriteString(strings.Join(cells, " | ") + "\n")
	}
	return b.String()
}
