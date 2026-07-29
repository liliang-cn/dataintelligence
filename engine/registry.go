package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Def declares one governed database: a semantic model over a warehouse.
type Def struct {
	ID    string // stable name callers select by ("shop", "conglomerate")
	Model string // semantic model YAML path
	DSN   string // warehouse DSN; the scheme picks driver and dialect
}

// Registry serves several governed databases from one process.
//
// A semantic model is scoped to one warehouse, so "which model" and "which
// warehouse" are the same question — a database. One process per database was
// the alternative, and it does not survive contact with a real deployment:
// thirteen modelled databases meant thirteen configs, thirteen ports, and
// thirteen things to notice had died.
//
// Engines open lazily and are cached. Boot therefore costs no connections, and
// one unreachable warehouse fails that database's requests rather than
// preventing the service from starting at all — the twelve that are healthy
// keep serving.
type Registry struct {
	mu      sync.Mutex
	defs    map[string]Def
	open    map[string]*Engine
	order   []string // declaration order; the first is the default
	primary string
}

// NewRegistry validates the definitions and returns a registry. The first
// definition is the default for callers that name no database.
func NewRegistry(defs ...Def) (*Registry, error) {
	if len(defs) == 0 {
		return nil, fmt.Errorf("engine: registry needs at least one database")
	}
	r := &Registry{defs: make(map[string]Def, len(defs)), open: map[string]*Engine{}}
	for _, d := range defs {
		if d.ID == "" {
			return nil, fmt.Errorf("engine: database id is required")
		}
		if !validID(d.ID) {
			return nil, fmt.Errorf("engine: database id %q must be letters, digits, '-' or '_' — it appears in URLs", d.ID)
		}
		if _, dup := r.defs[d.ID]; dup {
			return nil, fmt.Errorf("engine: duplicate database id %q", d.ID)
		}
		if d.Model == "" || d.DSN == "" {
			return nil, fmt.Errorf("engine: database %q needs both a model and a dsn", d.ID)
		}
		r.defs[d.ID] = d
		r.order = append(r.order, d.ID)
	}
	r.primary = r.order[0]
	return r, nil
}

// IDs lists the configured databases in declaration order.
func (r *Registry) IDs() []string { return append([]string(nil), r.order...) }

// Default is the id used when a caller names none.
func (r *Registry) Default() string { return r.primary }

// Has reports whether an id is configured, without opening it.
func (r *Registry) Has(id string) bool {
	_, ok := r.defs[id]
	return ok
}

// Get returns the engine for id, opening it on first use. An empty id selects
// the default. The error names the unknown database and lists what exists,
// because a typo in a database name is otherwise indistinguishable from a
// warehouse that is down.
func (r *Registry) Get(ctx context.Context, id string) (*Engine, error) {
	if id == "" {
		id = r.primary
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.open[id]; ok {
		return e, nil
	}
	d, ok := r.defs[id]
	if !ok {
		ids := append([]string(nil), r.order...)
		sort.Strings(ids)
		return nil, fmt.Errorf("unknown database %q — configured: %s", id, strings.Join(ids, ", "))
	}
	e, err := New(ctx, d.Model, d.DSN)
	if err != nil {
		return nil, fmt.Errorf("database %q: %w", id, err)
	}
	r.open[id] = e
	return e, nil
}

// Close shuts every opened engine down, reporting the first failure but
// attempting all of them.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for id, e := range r.open {
		if err := e.Close(); err != nil && first == nil {
			first = fmt.Errorf("database %q: %w", id, err)
		}
		delete(r.open, id)
	}
	return first
}

// validID keeps ids safe to put in a URL path segment: the MCP endpoint for a
// database is /db/{id}, so an id with a slash or a space would silently route
// somewhere else.
func validID(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
