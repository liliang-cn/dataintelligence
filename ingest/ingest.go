// Package ingest maps a source into the warehouse: infer a mapping (source field
// → model field), run key/data checks, surface a structure diff, and land the
// rows. This is the whiteboard's "Source → CSV (mapping) → Model → rules → DB".
package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/liliang-cn/dataintelligence/connectors"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// FieldMap routes one source column to a target (model) column.
type FieldMap struct {
	Source string
	Target string
	Type   string // inferred source type, carried to the report
}

// Tag is a constant column written on every ingested row (e.g. _run_id), so a
// flow can later compensate (rollback) by deleting exactly the rows it landed.
type Tag struct {
	Col string
	Val string
}

// MappingPlan is how a source lands into a table.
type MappingPlan struct {
	Table    string
	Fields   []FieldMap
	Required []string // target columns that must be non-empty (key/data checks)
	Tags     []Tag    // constant provenance columns (e.g. _run_id) added to every row
}

// Report is the outcome of an ingest run (the run receipt / structure analysis).
type Report struct {
	Table       string
	RowsRead    int
	RowsLanded  int
	RowsSkipped int
	Unmapped    []string // source columns not routed anywhere
	Diff        []string // structure notes (new/unmapped columns, etc.)
	Errors      []string
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlnum.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

// InferMapping builds a mapping. With no targets it lands every column under a
// normalized name (zero-config). With targets it fuzzy-matches each target to a
// source column (normalized equality, then substring).
func InferMapping(schema connectors.SourceSchema, table string, targets []string) MappingPlan {
	plan := MappingPlan{Table: table}
	if len(targets) == 0 {
		for _, f := range schema.Fields {
			plan.Fields = append(plan.Fields, FieldMap{Source: f.Name, Target: normalize(f.Name), Type: f.Type})
		}
		return plan
	}
	for _, t := range targets {
		nt := normalize(t)
		var best *connectors.Field
		for i := range schema.Fields {
			nf := normalize(schema.Fields[i].Name)
			if nf == nt {
				best = &schema.Fields[i]
				break
			}
			if best == nil && (strings.Contains(nf, nt) || strings.Contains(nt, nf)) {
				best = &schema.Fields[i]
			}
		}
		if best != nil {
			plan.Fields = append(plan.Fields, FieldMap{Source: best.Name, Target: t, Type: best.Type})
		}
	}
	return plan
}

// Diff reports source columns that the plan does not route — the "AI Diff /
// structure analysis" surface (here, deterministic; an LLM can enrich it later).
func Diff(schema connectors.SourceSchema, plan MappingPlan) []string {
	mapped := map[string]bool{}
	for _, fm := range plan.Fields {
		mapped[fm.Source] = true
	}
	var out []string
	for _, f := range schema.Fields {
		if !mapped[f.Name] {
			out = append(out, fmt.Sprintf("unmapped source column %q (%s)", f.Name, f.Type))
		}
	}
	return out
}

// Run creates the target table (text columns; inferred types are reported) and
// lands the rows, applying Required key/data checks.
func Run(ctx context.Context, wh *warehouse.Warehouse, batch connectors.Batch, plan MappingPlan) (Report, error) {
	rep := Report{Table: plan.Table, RowsRead: len(batch.Rows), Unmapped: nil}
	rep.Diff = Diff(batch.Schema, plan)
	for _, d := range rep.Diff {
		rep.Unmapped = append(rep.Unmapped, d)
	}
	if len(plan.Fields) == 0 {
		return rep, fmt.Errorf("mapping is empty (no columns routed to %q)", plan.Table)
	}

	// CREATE TABLE IF NOT EXISTS
	var cols []string
	for _, fm := range plan.Fields {
		cols = append(cols, quote(fm.Target)+" TEXT")
	}
	for _, t := range plan.Tags {
		cols = append(cols, quote(t.Col)+" TEXT")
	}
	ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quote(plan.Table), strings.Join(cols, ", "))
	if _, err := wh.Exec(ctx, ddl); err != nil {
		return rep, err
	}

	required := map[string]bool{}
	for _, r := range plan.Required {
		required[r] = true
	}

	// Build a single multi-row INSERT.
	var targetCols []string
	for _, fm := range plan.Fields {
		targetCols = append(targetCols, quote(fm.Target))
	}
	for _, t := range plan.Tags {
		targetCols = append(targetCols, quote(t.Col))
	}
	var valuesSQL []string
	var args []any
	ph := 1
	for _, row := range batch.Rows {
		// key/data checks first — so a skipped row never advances the placeholder counter.
		skip := false
		for _, fm := range plan.Fields {
			if required[fm.Target] && strings.TrimSpace(row[fm.Source]) == "" {
				rep.RowsSkipped++
				rep.Errors = append(rep.Errors, fmt.Sprintf("row skipped: required %q empty", fm.Target))
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		group := make([]string, 0, len(plan.Fields)+len(plan.Tags))
		for _, fm := range plan.Fields {
			group = append(group, fmt.Sprintf("$%d", ph))
			ph++
			args = append(args, row[fm.Source])
		}
		for _, t := range plan.Tags {
			group = append(group, fmt.Sprintf("$%d", ph))
			ph++
			args = append(args, t.Val)
		}
		valuesSQL = append(valuesSQL, "("+strings.Join(group, ", ")+")")
	}
	if len(valuesSQL) == 0 {
		return rep, nil
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		quote(plan.Table), strings.Join(targetCols, ", "), strings.Join(valuesSQL, ", "))
	n, err := wh.Exec(ctx, insert, args...)
	if err != nil {
		return rep, err
	}
	rep.RowsLanded = int(n)
	return rep, nil
}

func quote(id string) string { return `"` + strings.ReplaceAll(id, `"`, `""`) + `"` }
