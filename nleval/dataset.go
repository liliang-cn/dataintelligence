// Package nleval is the natural-language evaluation closed-loop: a labeled
// question set, three-axis scoring (semantic / execution / result), governance
// probes, a CI gate, and a persisted accuracy dashboard.
//
// It drives the real grounding + governance + engine path end-to-end and grades
// each answer against a hand-written control query — never a hardcoded number —
// so the set stays correct as the warehouse evolves. When LLM creds are present,
// eval-go adds an LLM-judge groundedness/faithfulness layer on top.
package nleval

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Case is one labeled question plus its expected resolution and grading anchors.
type Case struct {
	Name     string `yaml:"name"`
	Question string `yaml:"question"`
	Category string `yaml:"category"`
	Role     string `yaml:"role"` // governance principal role; default "admin"

	ExpectMetrics []string `yaml:"expect_metrics"`
	ExpectDims    []string `yaml:"expect_dims"`
	Control       string   `yaml:"control"` // SQL whose result the answer must match

	ExpectClarify bool `yaml:"expect_clarify"` // ambiguity probe: grounder must ask back
	ExpectRefused bool `yaml:"expect_refused"` // governance probe: query must be refused
	NeedsLLM      bool `yaml:"needs_llm"`      // excluded from the gate when no LLM is wired
}

// Dataset is the loaded eval set.
type Dataset struct {
	Cases []Case `yaml:"cases"`
}

// Load reads the eval set from a YAML file.
func Load(path string) (*Dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ds Dataset
	if err := yaml.Unmarshal(b, &ds); err != nil {
		return nil, err
	}
	return &ds, nil
}
