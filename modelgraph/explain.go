package modelgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

// The two questions that come up during a delivery and that reading the model
// file does not answer, because both run backwards through it and several hops
// deep.
//
// Explain: "where does this number come from?" — every metric it derives from,
// every entity it touches, every column it reads.
//
// Impact: "this column is changing, what moves?" — the mirror image, and the
// one that gets asked at the worst moment, usually by a DBA with a migration
// already written.

// Reader is the part of the brain these need.
type Reader interface {
	Neighbors(ctx context.Context, nodeID string, opts graph.TraversalOptions) ([]*graph.GraphNode, error)
	ListNodes(ctx context.Context, filter *graph.GraphFilter) ([]*graph.GraphNode, error)
}

// Derivation is one step of an explanation.
type Derivation struct {
	Name  string
	Kind  string // metric | entity | column
	Depth int
	Via   string // the edge that brought it in
}

// Explain walks out from a metric and reports everything it rests on.
//
// Depth is reported so a reader can tell a metric's own columns from the
// columns of a metric it derives from — the difference between "this reads
// qty" and "this reads qty two definitions away", which is exactly the
// distinction somebody chasing a wrong number needs.
func Explain(ctx context.Context, r Reader, metric string, maxDepth int) ([]Derivation, error) {
	if maxDepth <= 0 {
		maxDepth = 4
	}
	start := id(TypeMetric, metric)
	if _, err := nodeExists(ctx, r, start, TypeMetric, metric); err != nil {
		return nil, err
	}
	seen := map[string]bool{start: true}
	var out []Derivation
	frontier := []string{start}

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, from := range frontier {
			hits, err := r.Neighbors(ctx, from, graph.TraversalOptions{
				MaxDepth:  1,
				Direction: "out",
				EdgeTypes: []string{DerivesFrom, MeasuredOn, ReadsColumn},
				Limit:     500,
			})
			if err != nil {
				return nil, fmt.Errorf("modelgraph: explain %s: %w", metric, err)
			}
			for _, h := range hits {
				if seen[h.ID] {
					continue
				}
				seen[h.ID] = true
				out = append(out, Derivation{
					Name: nameOf(h), Kind: shortKind(h.NodeType), Depth: depth, Via: viaFor(h.NodeType),
				})
				if h.NodeType == TypeMetric {
					next = append(next, h.ID)
				}
			}
		}
		frontier = next
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Affected is one thing a change would move.
type Affected struct {
	Metric string
	Hops   int // 1 = reads the column directly; 2+ = derives from one that does
}

// Impact walks in from a column and reports every metric that would move.
//
// `column` is `table.column`, the way a DBA names it. Metrics that read it
// directly come back at one hop; metrics that derive from those come back
// further out, and they are the ones nobody remembers — the whole reason for
// asking the graph instead of grepping the model.
func Impact(ctx context.Context, r Reader, column string, maxDepth int) ([]Affected, error) {
	if maxDepth <= 0 {
		maxDepth = 4
	}
	start := id(TypeColumn, column)
	if _, err := nodeExists(ctx, r, start, TypeColumn, column); err != nil {
		return nil, err
	}
	seen := map[string]bool{start: true}
	hops := map[string]int{}
	frontier := []string{start}

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, to := range frontier {
			hits, err := r.Neighbors(ctx, to, graph.TraversalOptions{
				MaxDepth:  1,
				Direction: "in",
				EdgeTypes: []string{ReadsColumn, DerivesFrom},
				Limit:     500,
			})
			if err != nil {
				return nil, fmt.Errorf("modelgraph: impact %s: %w", column, err)
			}
			for _, h := range hits {
				if seen[h.ID] {
					continue
				}
				seen[h.ID] = true
				if h.NodeType == TypeMetric {
					hops[nameOf(h)] = depth
					next = append(next, h.ID)
				}
			}
		}
		frontier = next
	}
	out := make([]Affected, 0, len(hops))
	for name, d := range hops {
		out = append(out, Affected{Metric: name, Hops: d})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].Metric < out[j].Metric
	})
	return out, nil
}

// nodeExists turns "no such metric" into a sentence rather than an empty
// result. An empty explanation and a misspelled name look identical otherwise,
// and the second is far more common.
func nodeExists(ctx context.Context, r Reader, nid, kind, name string) (*graph.GraphNode, error) {
	nodes, err := r.ListNodes(ctx, &graph.GraphFilter{NodeTypes: []string{kind}, Limit: 5000})
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.ID == nid {
			return n, nil
		}
	}
	what := strings.TrimPrefix(kind, "di_")
	return nil, fmt.Errorf("modelgraph: no %s named %q in the graph — has `di graph build` run since the model changed?", what, name)
}

func nameOf(n *graph.GraphNode) string {
	if n.Properties != nil {
		if s, ok := n.Properties["name"].(string); ok && s != "" {
			return s
		}
	}
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(
		n.ID, TypeMetric+":"), TypeColumn+":"), TypeEntity+":")
}

func shortKind(t string) string { return strings.TrimPrefix(t, "di_") }

func viaFor(nodeType string) string {
	switch nodeType {
	case TypeMetric:
		return DerivesFrom
	case TypeEntity:
		return MeasuredOn
	case TypeColumn:
		return ReadsColumn
	}
	return ""
}
