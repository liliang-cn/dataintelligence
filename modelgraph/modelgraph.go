// Package modelgraph projects a signed semantic model into the brain's graph.
//
// The third thing this product does, beside computing numbers and answering
// questions, is explain where a number comes from — not in prose, but as a
// structure somebody can query and argue with.
//
// # Why the graph is built from the model rather than extracted from text
//
// The obvious way to get a knowledge graph is to hand documents to a language
// model and ask it for entities and relations. That was measured on
// 2026-09-14 against the baseline of putting the corpus in a model's context
// and simply asking: extraction lost, and pressed to be exhaustive it invents
// edges that were never written down. A graph whose edges might be inventions
// is a graph nobody can act on, which defeats the purpose of having one.
//
// A semantic model is a different kind of source. Every entity, join, grain
// and metric in it was declared by a person and — since the registry — signed
// by one. Projecting it produces a graph where every edge traces to a line
// somebody wrote and approved:
//
//	一次合格率_统一口径
//	  ← (cleaning_out_qty_sum + machining_out_qty_sum
//	     - cleaning_rework_qty_sum - heat_treat_rework_qty_sum)
//	    / nullif(pouring_poured_qty_sum + forging_charged_qty_sum, 0)
//
// That is six derives_from edges, and the reason each exists is the formula
// itself. Nothing was inferred and nothing can be hallucinated, because the
// projection is mechanical: the same model always yields the same graph.
//
// # What it answers that reading the YAML does not
//
// A model file answers "what is this metric" by being read. It does not answer
// the questions that actually come up during a delivery, which run the other
// way and several hops deep:
//
//   - this column is about to change — which metrics move?
//   - this figure looks wrong — which tables did it touch?
//   - these two metrics disagree — where do their definitions diverge?
//
// On the model this was built against, 44 metrics over 13 entities with 12
// declared joins, those are not questions a person answers by reading a
// 17,000-byte YAML file. They are one traversal each.
//
// # The model hash is on every node and edge
//
// A graph that outlives the model it came from is a graph that lies. Every
// node and edge carries the hash of the model that produced it, so a
// projection can be told apart from its predecessor, and a fact in the graph
// can be traced to the registry entry that says who approved the definition
// it came from — the same join `di rollout attest` uses.
package modelgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
	semantic "github.com/liliang-cn/semantic-go"
)

// Node types. They are the model's own vocabulary, not a generic
// entity/concept scheme: a reader of the graph should see the words they wrote
// in the YAML.
const (
	TypeEntity    = "di_entity"
	TypeMetric    = "di_metric"
	TypeDimension = "di_dimension"
)

// Edge types, each one a statement the model makes.
const (
	// JoinsTo is a declared join between two entities. The cardinality rides
	// on the edge because a many_to_one and a one_to_many are the difference
	// between a correct number and a fanned-out one.
	JoinsTo = "joins_to"
	// MeasuredOn points a metric at the entity whose rows it aggregates.
	MeasuredOn = "measured_on"
	// GroupsBy points a dimension at the entity it belongs to.
	GroupsBy = "groups_by"
	// DerivesFrom points a metric at a metric it is computed from — a formula's
	// operands, or the metric a window metric transforms.
	DerivesFrom = "derives_from"
	// ReadsColumn points a metric or dimension at a physical column, written
	// as `table.column`. It is what makes "this column is changing, what
	// moves?" a traversal rather than a grep.
	ReadsColumn = "reads_column"
)

// TypeColumn is a physical column of the warehouse, the one node type that is
// not a model concept. It exists so impact analysis has something to start
// from: a DBA announcing a change names a column, not a metric.
const TypeColumn = "di_column"

// Projection is what one build wrote.
type Projection struct {
	ModelHash string
	Nodes     int
	Edges     int
}

// Store is the part of the brain this package needs.
type Store interface {
	UpsertNodesBatch(ctx context.Context, nodes []*graph.GraphNode) (*graph.BatchResult, error)
	UpsertEdgesBatch(ctx context.Context, edges []*graph.GraphEdge) (*graph.BatchResult, error)
}

// A batch write reports per-row failures in a field rather than returning
// them, so a batch where every single row was rejected comes back as a
// successful call. The first version of Build trusted that and wrote nothing
// at all — every node was refused for having no vector, Build reported the
// node count it had intended to write, and the failure only surfaced when a
// traversal found an empty graph.
//
// This graph is structural: a node's meaning is its edges, not its position in
// a vector space, and nothing here searches it by similarity. But the store
// stores vectors, so each node gets a deterministic one derived from its id —
// ceremony rather than meaning, and said so here rather than left for somebody
// to mistake for an embedding.
func checked(what string, res *graph.BatchResult, err error) error {
	if err != nil {
		return fmt.Errorf("modelgraph: write %s: %w", what, err)
	}
	if res != nil && res.FailedCount > 0 {
		first := "unknown"
		if len(res.Errors) > 0 && res.Errors[0] != nil {
			first = res.Errors[0].Error()
		}
		return fmt.Errorf("modelgraph: %d of the %s were rejected by the store: %s",
			res.FailedCount, what, first)
	}
	return nil
}

// vectorFor is the deterministic placeholder described above. Width comes from
// the store, because a vector of the wrong length is rejected row by row and
// reported in a field nobody reads.
func vectorFor(id string, dim int) []float32 {
	if dim < 1 {
		dim = 1
	}
	v := make([]float32, dim)
	h := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		h = (h ^ uint32(id[i])) * 16777619
	}
	for i := range v {
		h = h*1664525 + 1013904223
		v[i] = float32(h%2000)/1000 - 1
	}
	return v
}

// Build projects the model into the graph and reports what it wrote.
//
// It is idempotent: node and edge ids are derived from the model's own names,
// so re-projecting the same model overwrites rather than accumulates. A
// changed model writes new nodes beside the old ones — they carry different
// hashes, and Prune removes the ones no longer current.
func Build(ctx context.Context, s Store, m *semantic.Model, modelHash string, dim int) (*Projection, error) {
	if m == nil {
		return nil, fmt.Errorf("modelgraph: no model")
	}
	b := &builder{hash: modelHash, dim: dim, seen: map[string]bool{}}

	for i := range m.Entities {
		e := &m.Entities[i]
		b.node(TypeEntity, e.Name, e.Name, map[string]any{
			"table":       e.Table,
			"primary_key": strings.Join(e.PrimaryKey, ", "),
		})
	}
	for i := range m.Joins {
		j := &m.Joins[i]
		b.edge(JoinsTo, id(TypeEntity, j.From), id(TypeEntity, j.To), map[string]any{
			"cardinality": j.Cardinality,
			"from_key":    strings.Join(j.FromKey, ", "),
			"to_key":      strings.Join(j.ToKey, ", "),
		})
	}
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		b.node(TypeDimension, d.Name, d.Name, map[string]any{
			"entity": d.Entity, "column": d.Column, "type": d.Type,
			"masked": d.Mask != "",
		})
		if d.Entity != "" {
			b.edge(GroupsBy, id(TypeDimension, d.Name), id(TypeEntity, d.Entity), nil)
		}
		b.columns(m, d.Entity, d.Column, id(TypeDimension, d.Name))
	}
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		b.node(TypeMetric, mt.Name, mt.Description, map[string]any{
			"entity": mt.Entity, "agg": mt.Agg, "expr": mt.Expr,
			"formula": mt.Formula, "of": mt.Of, "window": mt.Window,
			"gated": len(mt.Roles) > 0,
		})
		if mt.Entity != "" {
			b.edge(MeasuredOn, id(TypeMetric, mt.Name), id(TypeEntity, mt.Entity), nil)
		}
		b.columns(m, mt.Entity, mt.Expr, id(TypeMetric, mt.Name))
	}
	// Derivation edges come last: they point metric at metric, so every metric
	// node has to exist first for the reference to be meaningful.
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		for _, ref := range derivedFrom(m, mt) {
			b.edge(DerivesFrom, id(TypeMetric, mt.Name), id(TypeMetric, ref), map[string]any{
				"via": viaOf(mt),
			})
		}
	}

	nres, nerr := s.UpsertNodesBatch(ctx, b.nodes)
	if err := checked("nodes", nres, nerr); err != nil {
		return nil, err
	}
	eres, eerr := s.UpsertEdgesBatch(ctx, b.edges)
	if err := checked("edges", eres, eerr); err != nil {
		return nil, err
	}
	return &Projection{ModelHash: modelHash, Nodes: len(b.nodes), Edges: len(b.edges)}, nil
}

// derivedFrom is the metrics this metric is computed from.
//
// A formula names them in an SQL expression, so they are found by looking for
// every other metric's name as a whole word — which is exact rather than
// clever, because the model's own resolver treats a formula the same way.
// A window metric names exactly one, in Of.
func derivedFrom(m *semantic.Model, mt *semantic.Metric) []string {
	if strings.TrimSpace(mt.Of) != "" {
		return []string{strings.TrimSpace(mt.Of)}
	}
	if strings.TrimSpace(mt.Formula) == "" {
		return nil
	}
	words := map[string]bool{}
	for _, w := range splitIdents(mt.Formula) {
		words[w] = true
	}
	var out []string
	for i := range m.Metrics {
		name := m.Metrics[i].Name
		if name != mt.Name && words[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func viaOf(mt *semantic.Metric) string {
	if mt.Of != "" {
		return mt.Window
	}
	return "formula"
}

// splitIdents pulls the identifier-shaped runs out of an SQL expression. A
// metric name can contain CJK, digits and underscores, so the split is on
// what an identifier is not.
func splitIdents(expr string) []string {
	return strings.FieldsFunc(expr, func(r rune) bool {
		switch {
		case r == '_', r >= '0' && r <= '9',
			r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			return false
		case r > 0x7F: // CJK and anything else non-ASCII is part of a name
			return false
		}
		return true
	})
}

type builder struct {
	hash  string
	dim   int
	nodes []*graph.GraphNode
	edges []*graph.GraphEdge
	seen  map[string]bool
}

func id(kind, name string) string { return kind + ":" + name }

func (b *builder) node(kind, name, content string, props map[string]any) {
	nid := id(kind, name)
	if b.seen[nid] {
		return
	}
	b.seen[nid] = true
	if props == nil {
		props = map[string]any{}
	}
	props["name"] = name
	props["model_hash"] = b.hash
	b.nodes = append(b.nodes, &graph.GraphNode{
		ID: nid, NodeType: kind, Content: content, Properties: props,
		Vector: vectorFor(nid, b.dim),
	})
}

func (b *builder) edge(kind, from, to string, props map[string]any) {
	if from == "" || to == "" {
		return
	}
	if props == nil {
		props = map[string]any{}
	}
	props["model_hash"] = b.hash
	b.edges = append(b.edges, &graph.GraphEdge{
		ID: kind + ":" + from + "->" + to, FromNodeID: from, ToNodeID: to,
		EdgeType: kind, Weight: 1, Properties: props,
	})
}

// columns records which physical columns an expression reads, as
// `table.column` nodes hanging off the entity the metric or dimension is on.
//
// It is deliberately conservative: only identifiers that are actual column
// names of that entity's table become edges. An SQL function name or a
// literal is not a column, and inventing a node for `nullif` would put noise
// into the one graph that is supposed to contain nothing invented.
func (b *builder) columns(m *semantic.Model, entity, expr, from string) {
	if entity == "" || strings.TrimSpace(expr) == "" {
		return
	}
	var table string
	for i := range m.Entities {
		if m.Entities[i].Name == entity {
			table = m.Entities[i].Table
			break
		}
	}
	if table == "" {
		return
	}
	known := columnsOf(m, entity)
	for _, w := range splitIdents(expr) {
		if !known[w] {
			continue
		}
		col := table + "." + w
		b.node(TypeColumn, col, col, map[string]any{"table": table, "column": w})
		b.edge(ReadsColumn, from, id(TypeColumn, col), nil)
	}
}

// columnsOf is every column of an entity the model itself mentions: its
// primary key, its join keys, and the columns its dimensions sit on. The model
// is not a catalogue of the warehouse, so this is what it knows — and a column
// the model never mentions is one no metric can be reading.
func columnsOf(m *semantic.Model, entity string) map[string]bool {
	out := map[string]bool{}
	for i := range m.Entities {
		e := &m.Entities[i]
		if e.Name != entity {
			continue
		}
		for _, k := range e.PrimaryKey {
			out[k] = true
		}
	}
	for i := range m.Joins {
		j := &m.Joins[i]
		if j.From == entity {
			for _, k := range j.FromKey {
				out[k] = true
			}
		}
		if j.To == entity {
			for _, k := range j.ToKey {
				out[k] = true
			}
		}
	}
	for i := range m.Dimensions {
		if m.Dimensions[i].Entity == entity {
			out[m.Dimensions[i].Column] = true
		}
	}
	// And the columns the entity's own metrics aggregate, which is how a
	// measure column like `qty` gets into the set at all.
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		if mt.Entity != entity || mt.Expr == "" {
			continue
		}
		for _, w := range splitIdents(mt.Expr) {
			if !isSQLWord(w) {
				out[w] = true
			}
		}
	}
	return out
}

// isSQLWord keeps function names and keywords out of the column set. The list
// is short on purpose: a name this misses becomes one extra column node, and a
// name it wrongly includes silently drops a real edge — so it errs towards
// letting things through.
var sqlWords = map[string]bool{
	"nullif": true, "coalesce": true, "case": true, "when": true, "then": true,
	"else": true, "end": true, "cast": true, "as": true, "and": true, "or": true,
	"not": true, "null": true, "distinct": true, "count": true, "sum": true,
	"avg": true, "min": true, "max": true, "abs": true, "round": true,
}

func isSQLWord(w string) bool { return sqlWords[strings.ToLower(w)] }
