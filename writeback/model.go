package writeback

import (
	"fmt"
	"os"
	"time"

	semantic "github.com/liliang-cn/semantic-go"
	"gopkg.in/yaml.v3"
)

func nowNs() int64 { return time.Now().UnixNano() }

// modelDoc mirrors the semantic model YAML structurally (yaml.Node preserves the
// existing entries) so we can append a generated metric/dimension and re-marshal.
type modelDoc struct {
	Entities   []yaml.Node `yaml:"entities,omitempty"`
	Joins      []yaml.Node `yaml:"joins,omitempty"`
	Dimensions []yaml.Node `yaml:"dimensions,omitempty"`
	Metrics    []yaml.Node `yaml:"metrics,omitempty"`
}

// mergeModel parses the current model + the proposed fragment and returns the
// merged YAML bytes (the generated metric/dimension appended to the right list).
func (e *Engine) mergeModel(mc *ModelChange) ([]byte, error) {
	cur, err := os.ReadFile(e.ModelPath)
	if err != nil {
		return nil, err
	}
	var doc modelDoc
	if err := yaml.Unmarshal(cur, &doc); err != nil {
		return nil, fmt.Errorf("parse current model: %w", err)
	}
	var frag yaml.Node
	if err := yaml.Unmarshal([]byte(mc.YAML), &frag); err != nil {
		return nil, fmt.Errorf("parse proposed fragment: %w", err)
	}
	// Unwrap the document node, then collect the entry node(s): a fragment written
	// as a YAML list item ("- name: …") parses to a sequence, while a bare mapping
	// ("name: …") parses to a mapping — accept either.
	node := &frag
	if frag.Kind == yaml.DocumentNode && len(frag.Content) > 0 {
		node = frag.Content[0]
	}
	var entries []yaml.Node
	switch node.Kind {
	case yaml.SequenceNode:
		for _, c := range node.Content {
			entries = append(entries, *c)
		}
	case yaml.MappingNode:
		entries = append(entries, *node)
	default:
		return nil, fmt.Errorf("fragment must be a metric/dimension mapping, got yaml kind %d", node.Kind)
	}
	switch mc.Kind {
	case "metric":
		doc.Metrics = append(doc.Metrics, entries...)
	case "dimension":
		doc.Dimensions = append(doc.Dimensions, entries...)
	default:
		return nil, fmt.Errorf("unknown model change kind %q", mc.Kind)
	}
	return yaml.Marshal(doc)
}

// planModel validates a transform proposal by merging it into a candidate model
// and confirming the model still loads AND the new metric compiles — a generated
// transform that won't compile never reaches review as "ready".
func (e *Engine) planModel(prop *Proposal) error {
	mc := prop.Model
	if mc == nil || mc.Name == "" || mc.YAML == "" {
		return fmt.Errorf("model change needs kind, name and yaml")
	}
	merged, err := e.mergeModel(mc)
	if err != nil {
		return err
	}
	m, err := semantic.Load(merged)
	if err != nil {
		return fmt.Errorf("candidate model invalid: %w", err)
	}
	note := fmt.Sprintf("candidate model loads; adds %s %q", mc.Kind, mc.Name)
	if mc.Kind == "metric" {
		if _, err := semantic.Compile(m, semantic.Query{Metrics: []string{mc.Name}}, semantic.Postgres{}); err != nil {
			return fmt.Errorf("new metric %q does not compile: %w", mc.Name, err)
		}
		note += " — and compiles"
	}
	prop.Preview = &Preview{Note: note}
	return nil
}

// commitModel backs up the current model and writes the merged one. Rollback of a
// transform is restoring the .bak (the change is to versioned config, not data).
func (e *Engine) commitModel(prop *Proposal) error {
	merged, err := e.mergeModel(prop.Model)
	if err != nil {
		return err
	}
	if _, err := semantic.Load(merged); err != nil {
		return fmt.Errorf("refusing to write an invalid model: %w", err)
	}
	cur, err := os.ReadFile(e.ModelPath)
	if err != nil {
		return err
	}
	bak := fmt.Sprintf("%s.bak-%d", e.ModelPath, e.now())
	if err := os.WriteFile(bak, cur, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(e.ModelPath, merged, 0o644); err != nil {
		return err
	}
	prop.Note = fmt.Sprintf("model updated; previous backed up to %s", bak)
	return nil
}
