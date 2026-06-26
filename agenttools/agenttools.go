// Package agenttools is the single source of truth for the platform's
// agent-callable capabilities. Both the MCP server (external agents) and the
// in-process copilot (di copilot, the web console) delegate here, so internal
// and external agents share one governed toolset with identical behavior.
package agenttools

import (
	"context"
	"fmt"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/reconcile"
)

// MetricInfo describes one metric for list_metrics.
type MetricInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Synonyms    []string `json:"synonyms,omitempty"`
}

// ListMetrics returns the semantic model's metrics.
func ListMetrics(eng *engine.Engine) []MetricInfo {
	out := make([]MetricInfo, 0, len(eng.Model.Metrics))
	for i := range eng.Model.Metrics {
		m := &eng.Model.Metrics[i]
		out = append(out, MetricInfo{m.Name, m.Description, m.Synonyms})
	}
	return out
}

// Dimensions returns the dimensions a metric can be grouped by without a fanout.
func Dimensions(eng *engine.Engine, metric string) ([]string, error) {
	return eng.Model.DimensionsFor(metric)
}

// Query runs a governed semantic query as the given principal.
func Query(ctx context.Context, eng *engine.Engine, pol governance.Policy, p governance.Principal, q semantic.Query) (*engine.Answer, error) {
	return governance.Query(ctx, eng, q, p, pol)
}

// DescribeWarehouse lists the warehouse tables and their column counts.
func DescribeWarehouse(ctx context.Context, eng *engine.Engine) ([]string, error) {
	s, err := modelgen.Introspect(ctx, eng.WH)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(s.Tables))
	for _, t := range s.Tables {
		out = append(out, fmt.Sprintf("%s (%d cols)", t.Name, len(t.Columns)))
	}
	return out, nil
}

// Conflict is one cross-source conflict-check summary.
type Conflict struct {
	Check     string `json:"check"`
	Severity  string `json:"severity"`
	Conflicts int    `json:"conflicts"`
}

// HealthCheck runs the conflict checks and returns per-check counts.
func HealthCheck(ctx context.Context, eng *engine.Engine, checksPath string) ([]Conflict, error) {
	cs, err := reconcile.Load(checksPath)
	if err != nil {
		return nil, err
	}
	results, err := reconcile.Run(ctx, eng.WH, cs, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, 0, len(results))
	for _, r := range results {
		out = append(out, Conflict{r.Check.Name, r.Check.Severity, r.Count()})
	}
	return out, nil
}
