package modelgen

import (
	"context"
	"fmt"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
	"gopkg.in/yaml.v3"
)

// AskFunc is the LLM call used to refine a draft (prompt → completion). Pass nil
// for a purely heuristic, offline draft.
type AskFunc func(ctx context.Context, prompt string) (string, error)

// Generate builds a semantic-model draft from an introspected schema. It always
// produces a valid heuristic baseline; when ask is non-nil it asks the LLM to
// improve descriptions, synonyms, and metric selection, falling back to the
// baseline if the model can't parse or validate. Returns the model plus lint
// issues so the reviewer sees what still needs a human touch.
func Generate(ctx context.Context, schema *Schema, ask AskFunc) (*semantic.Model, []semantic.Issue, error) {
	base, err := HeuristicModel(schema)
	if err != nil {
		return nil, nil, err
	}
	model := base
	if ask != nil {
		if refined := refineWithLLM(ctx, schema, base, ask); refined != nil {
			model = refined
		}
	}
	return model, semantic.Lint(model), nil
}

// HeuristicModel derives a model with no LLM: entities per table, joins from
// foreign keys, dimensions from categorical/time columns, and sum/count metrics
// from numeric measures. Names follow the conventions the compiler expects
// (singular entities, <entity>_<column> dimensions).
func HeuristicModel(schema *Schema) (*semantic.Model, error) {
	m := &semantic.Model{}
	tableEntity := map[string]string{}
	for _, t := range schema.Tables {
		tableEntity[t.Name] = entityName(t.Name)
	}

	for _, t := range schema.Tables {
		ent := tableEntity[t.Name]
		pk := t.PrimaryKey
		if pk == "" {
			pk = guessKey(t)
		}
		if pk == "" {
			continue // can't model a table with no identifiable key
		}
		m.Entities = append(m.Entities, semantic.Entity{Name: ent, Table: t.Name, PrimaryKey: pk})

		key := map[string]bool{pk: true}
		for _, fk := range t.ForeignKeys {
			key[fk.Column] = true
			if refEnt, ok := tableEntity[fk.RefTable]; ok {
				m.Joins = append(m.Joins, semantic.Join{
					From: ent, To: refEnt, FromKey: fk.Column, ToKey: fk.RefColumn, Cardinality: "many_to_one",
				})
			}
		}

		// A count metric per entity (distinct primary key).
		m.Metrics = append(m.Metrics, semantic.Metric{
			Name: ent + "_count", Entity: ent, Agg: "count_distinct", Expr: pk,
			Description: fmt.Sprintf("Number of distinct %s (auto-generated; review).", ent),
			Synonyms:    []string{ent + " count", "number of " + t.Name}, Additivity: semantic.NonAdditive,
		})

		for _, c := range t.Columns {
			if key[c.Name] {
				continue // keys are join columns, not dims/metrics
			}
			switch kindOf(c.Type) {
			case kindTime:
				m.Dimensions = append(m.Dimensions, semantic.Dimension{
					Name: dimName(ent, c.Name), Entity: ent, Column: c.Name, Type: "time",
				})
			case kindText:
				m.Dimensions = append(m.Dimensions, semantic.Dimension{
					Name: dimName(ent, c.Name), Entity: ent, Column: c.Name, Type: "categorical",
				})
			case kindNumeric:
				m.Metrics = append(m.Metrics, semantic.Metric{
					Name: ent + "_" + c.Name + "_sum", Entity: ent, Agg: "sum", Expr: c.Name,
					Description: fmt.Sprintf("Sum of %s.%s (auto-generated; confirm it is additive).", t.Name, c.Name),
					Synonyms:    []string{strings.ReplaceAll(c.Name, "_", " ")},
				})
			}
		}
	}
	if len(m.Entities) == 0 {
		return nil, fmt.Errorf("no modelable tables found (need a primary key)")
	}
	if err := m.Index(); err != nil {
		return nil, fmt.Errorf("generated model is invalid: %w", err)
	}
	return m, nil
}

// refineWithLLM asks the model to return an improved full model YAML; the result
// is used only if it parses and validates, so a bad completion never breaks the
// draft.
func refineWithLLM(ctx context.Context, schema *Schema, base *semantic.Model, ask AskFunc) *semantic.Model {
	baseYAML, _ := yaml.Marshal(base)
	prompt := fmt.Sprintf(`You are a data modeling expert. Improve this auto-generated semantic-layer model.
Schema (tables, columns, foreign keys):
%s

Current draft (YAML):
%s

Return ONLY the improved model as YAML with the SAME structure (entities/joins/dimensions/metrics).
Rules: keep table/column/key names EXACTLY as in the schema; write clear one-line descriptions;
add natural-language synonyms; mark ratio/derived metrics with additivity: non_additive; drop
metrics that sum a non-additive column (e.g. unit prices); do not invent tables or columns.`,
		describeSchema(schema), string(baseYAML))

	out, err := ask(ctx, prompt)
	if err != nil {
		return nil
	}
	out = stripFence(out)
	refined, err := semantic.Load([]byte(out))
	if err != nil {
		return nil
	}
	if err := refined.Index(); err != nil {
		return nil
	}
	return refined
}

func describeSchema(s *Schema) string {
	var b strings.Builder
	for _, t := range s.Tables {
		fmt.Fprintf(&b, "- %s (pk: %s)\n", t.Name, t.PrimaryKey)
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "    %s %s\n", c.Name, c.Type)
		}
		for _, fk := range t.ForeignKeys {
			fmt.Fprintf(&b, "    FK %s -> %s.%s\n", fk.Column, fk.RefTable, fk.RefColumn)
		}
	}
	return b.String()
}

// ToYAML serializes a model for review/output.
func ToYAML(m *semantic.Model) ([]byte, error) { return yaml.Marshal(m) }

// --- heuristics ---

type colKind int

const (
	kindOther colKind = iota
	kindTime
	kindText
	kindNumeric
)

func kindOf(dataType string) colKind {
	t := strings.ToLower(dataType)
	switch {
	case strings.Contains(t, "timestamp"), t == "date", strings.HasPrefix(t, "time"):
		return kindTime
	case strings.Contains(t, "char"), t == "text", t == "boolean", t == "uuid":
		return kindText
	case strings.Contains(t, "int"), strings.Contains(t, "numeric"), strings.Contains(t, "decimal"),
		t == "real", strings.Contains(t, "double"), t == "money":
		return kindNumeric
	default:
		return kindOther
	}
}

// entityName singularizes a table name (orders→order, order_items→order_item,
// stores→store) so generated SQL reads naturally.
func entityName(table string) string {
	if strings.HasSuffix(table, "ies") {
		return table[:len(table)-3] + "y"
	}
	if strings.HasSuffix(table, "ss") {
		return table
	}
	if strings.HasSuffix(table, "s") && len(table) > 3 {
		return table[:len(table)-1]
	}
	return table
}

func dimName(entity, col string) string {
	if strings.HasPrefix(col, entity+"_") {
		return col
	}
	return entity + "_" + col
}

// guessKey finds a likely primary key when none is declared.
func guessKey(t Table) string {
	for _, want := range []string{"id", entityName(t.Name) + "_id", t.Name + "_id"} {
		for _, c := range t.Columns {
			if c.Name == want {
				return c.Name
			}
		}
	}
	return ""
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
