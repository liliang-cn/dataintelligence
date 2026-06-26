// Package reconcile finds cross-source data conflicts and (optionally) has an LLM
// triage them. The detection is deterministic SQL — declared as data in a checks
// file, so the platform stays domain-neutral; the AI only adds judgment on top of
// facts it can't invent: likely cause, system-of-record, recommended action.
package reconcile

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Check is one conflict detector: SQL that returns the conflicting rows.
type Check struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Severity    string `yaml:"severity"` // info | warn | critical
	SQL         string `yaml:"sql"`
}

// CheckSet is the loaded set.
type CheckSet struct {
	Checks []Check `yaml:"checks"`
}

// Result is one check's outcome.
type Result struct {
	Check   Check
	Columns []string
	Rows    [][]any
	Triage  string // LLM judgment (empty when no LLM)
}

func (r Result) Count() int { return len(r.Rows) }

// Load reads a checks file.
func Load(path string) (*CheckSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cs CheckSet
	if err := yaml.Unmarshal(b, &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}

// AskFunc is the LLM call used for triage (nil = detection only).
type AskFunc func(ctx context.Context, prompt string) (string, error)

// Run executes every check; when ask is non-nil, each conflict that fired is sent
// to the LLM for triage.
func Run(ctx context.Context, wh *warehouse.Warehouse, cs *CheckSet, ask AskFunc) ([]Result, error) {
	var out []Result
	for _, c := range cs.Checks {
		res, err := wh.Query(ctx, c.SQL)
		if err != nil {
			return nil, fmt.Errorf("check %q: %w", c.Name, err)
		}
		r := Result{Check: c, Columns: res.Columns, Rows: res.Rows}
		if ask != nil && r.Count() > 0 {
			if t, err := ask(ctx, triagePrompt(r)); err == nil {
				r.Triage = strings.TrimSpace(t)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// triagePrompt frames a conflict for the LLM: the facts (check + a sample of the
// conflicting rows) and a tight ask for cause + system-of-record + action.
func triagePrompt(r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a data-quality analyst reconciling data across enterprise systems.\n")
	fmt.Fprintf(&b, "Conflict check: %s\nWhat it means: %s\nConflicting rows: %d\n\n", r.Check.Name, r.Check.Description, r.Count())
	fmt.Fprintf(&b, "Columns: %s\nSample:\n", strings.Join(r.Columns, " | "))
	for i, row := range r.Rows {
		if i >= 5 {
			break
		}
		cells := make([]string, len(row))
		for j, v := range row {
			cells[j] = fmt.Sprintf("%v", v)
		}
		fmt.Fprintf(&b, "  %s\n", strings.Join(cells, " | "))
	}
	b.WriteString("\nIn 2-3 sentences: the most likely cause, which system should be treated as the " +
		"system-of-record, and the recommended remediation. Be specific and concise.")
	return b.String()
}
