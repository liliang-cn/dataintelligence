package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/liliang-cn/dataintelligence/connectors"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// A flow is data, not code. The engine offers four generic step PRIMITIVES;
// a YAML manifest composes them into a domain workflow. No business logic lives
// in the platform — it lives in the (customer/example) flow file.
//
//	type: ingest  — read a named source (from the sources manifest) into a table
//	type: sql     — run do SQL; undo runs the compensating SQL on rollback
//	type: mutate  — snapshot rows, apply a change, restore them on rollback
//	type: human   — pause for approval
type StepSpec struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`

	Source string `yaml:"source,omitempty"` // ingest: source name in the sources manifest
	Table  string `yaml:"table,omitempty"`  // ingest: staging table

	Do   string `yaml:"do,omitempty"`   // sql: forward statement(s)
	Undo string `yaml:"undo,omitempty"` // sql: compensating statement(s)

	Snapshot string `yaml:"snapshot,omitempty"` // mutate: SELECT rows to save (incl. the key col)
	Key      string `yaml:"key,omitempty"`      // mutate: primary-key column in the snapshot
	Apply    string `yaml:"apply,omitempty"`    // mutate: the mutation statement
	Restore  string `yaml:"restore,omitempty"`  // mutate: per-row restore, uses ${col} tokens
}

// FlowSpec is one named, file-defined workflow.
type FlowSpec struct {
	Name  string     `yaml:"name"`
	Steps []StepSpec `yaml:"steps"`
}

// Deps are the platform capabilities a flow's steps may use — injected by the
// caller so the flow package stays decoupled from how sources are resolved.
type Deps struct {
	ResolveSource func(name string) (connectors.Source, error)
}

// LoadDir loads every *.yaml flow file in dir and compiles it to []Step.
func LoadDir(dir string, deps Deps) (map[string][]Step, error) {
	out := map[string][]Step{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !(strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var spec FlowSpec
		if err := yaml.Unmarshal(b, &spec); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		steps, err := spec.compile(deps)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.Name, err)
		}
		out[spec.Name] = steps
	}
	return out, nil
}

func (f FlowSpec) compile(deps Deps) ([]Step, error) {
	steps := make([]Step, 0, len(f.Steps))
	for _, s := range f.Steps {
		st, err := s.toStep(deps)
		if err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	return steps, nil
}

func (s StepSpec) toStep(deps Deps) (Step, error) {
	switch s.Type {
	case "human":
		return Step{Name: s.Name, Human: true}, nil
	case "ingest":
		return s.ingestStep(deps), nil
	case "sql":
		return s.sqlStep(), nil
	case "mutate":
		return s.mutateStep(), nil
	default:
		return Step{}, fmt.Errorf("step %q: unknown type %q", s.Name, s.Type)
	}
}

func (s StepSpec) ingestStep(deps Deps) Step {
	table := s.Table
	return Step{
		Name: s.Name,
		Do: func(ctx context.Context, rc *RunContext) error {
			if deps.ResolveSource == nil {
				return fmt.Errorf("ingest step %q: no source resolver configured", s.Name)
			}
			src, err := deps.ResolveSource(s.Source)
			if err != nil {
				return err
			}
			batch, err := src.Read(ctx)
			if err != nil {
				return err
			}
			n, err := connectors.Stage(ctx, rc.WH, table, batch)
			if err != nil {
				return err
			}
			rc.State[s.Name+".rows"] = n
			return nil
		},
		Undo: func(ctx context.Context, rc *RunContext) error {
			_, err := rc.WH.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, table))
			return err
		},
	}
}

func (s StepSpec) sqlStep() Step {
	return Step{
		Name: s.Name,
		Do:   func(ctx context.Context, rc *RunContext) error { return execMulti(ctx, rc.WH, s.Do) },
		Undo: func(ctx context.Context, rc *RunContext) error {
			if strings.TrimSpace(s.Undo) == "" {
				return nil
			}
			return execMulti(ctx, rc.WH, s.Undo)
		},
	}
}

func (s StepSpec) mutateStep() Step {
	key := s.Key
	return Step{
		Name: s.Name,
		Do: func(ctx context.Context, rc *RunContext) error {
			// Snapshot the rows the mutation will touch (for rollback).
			res, err := rc.WH.Query(ctx, s.Snapshot)
			if err != nil {
				return err
			}
			before := make([]map[string]any, 0, len(res.Rows))
			for _, row := range res.Rows {
				m := map[string]any{}
				for i, c := range res.Columns {
					m[c] = fmt.Sprintf("%v", row[i])
				}
				before = append(before, m)
			}
			rc.State[s.Name+".before"] = before
			_, err = rc.WH.Exec(ctx, s.Apply)
			return err
		},
		Undo: func(ctx context.Context, rc *RunContext) error {
			before := asMaps(rc.State[s.Name+".before"])
			for _, row := range before {
				stmt, args := bindTokens(s.Restore, row, key)
				if _, err := rc.WH.Exec(ctx, stmt, args...); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// --- helpers ---

var tokenRe = regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`)

// bindTokens turns "UPDATE t SET c=${c} WHERE id=${id}" + a row into a
// parameterized statement (no string interpolation of values).
func bindTokens(tmpl string, row map[string]any, _ string) (string, []any) {
	var args []any
	n := 0
	stmt := tokenRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		col := tokenRe.FindStringSubmatch(m)[1]
		n++
		args = append(args, row[col])
		return fmt.Sprintf("$%d", n)
	})
	return stmt, args
}

// execMulti runs one or more ';'-separated statements (DDL/DML, no params).
func execMulti(ctx context.Context, wh *warehouse.Warehouse, sql string) error {
	for _, stmt := range strings.Split(sql, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := wh.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func asMaps(v any) []map[string]any {
	var out []map[string]any
	switch arr := v.(type) {
	case []any:
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case []map[string]any:
		out = arr
	}
	return out
}
