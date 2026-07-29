package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store persists databases registered at runtime.
//
// Config-file databases and runtime-registered ones are kept apart on purpose.
// A deployment's YAML is the operator's statement of intent and should not be
// rewritten by an API call; what a product's setup wizard adds belongs in
// state, beside it. On boot both are loaded, config first, so an id declared in
// both resolves to what the operator wrote.
type Store struct {
	path string
}

// NewStore persists to path. An empty path disables persistence, which is what
// a stateless deployment wants: databases come from config and nothing else.
func NewStore(path string) *Store { return &Store{path: path} }

// Enabled reports whether runtime registration can be persisted.
func (s *Store) Enabled() bool { return s != nil && s.path != "" }

// Load reads the persisted databases. A missing file is not an error — it is
// the normal state before anyone has added one. A corrupt file is reported
// rather than silently treated as empty: losing a customer's saved connections
// without saying so is worse than refusing to start.
func (s *Store) Load() ([]Def, error) {
	if !s.Enabled() {
		return nil, nil
	}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var defs []Def
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return defs, nil
}

// Save writes the databases, sorted so the file does not churn. The file holds
// DSNs — credentials — so it is written 0600 and its directory 0700.
func (s *Store) Save(defs []Def) error {
	if !s.Enabled() {
		return nil
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	raw, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
