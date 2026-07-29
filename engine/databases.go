package engine

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/liliang-cn/dataintelligence/grounding"
)

// Databases pairs each governed database with its grounding index.
//
// Grounding is model-scoped — the retrieval index is built from one model's
// metrics and dimensions — so a second database needs a second index. Both open
// lazily and together, because a caller that has resolved a database always
// needs both and neither is cheap enough to build for databases nobody asks
// about.
type Databases struct {
	*Registry
	indexDir  string
	exemplars string

	mu        sync.Mutex
	grounders map[string]*grounding.Grounder
}

// NewDatabases wraps a registry with per-database grounding. indexDir holds one
// SQLite index per database; exemplars is an optional few-shot bank applied to
// every database.
func NewDatabases(reg *Registry, indexDir, exemplars string) *Databases {
	return &Databases{Registry: reg, indexDir: indexDir, exemplars: exemplars,
		grounders: map[string]*grounding.Grounder{}}
}

// Resolve returns the engine and grounder for id ("" selects the default).
// The grounder may be nil if its index could not be built: grounding is an
// NL convenience, and losing it should not take governed querying down with it.
func (d *Databases) Resolve(ctx context.Context, id string) (*Engine, *grounding.Grounder, error) {
	eng, err := d.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if id == "" {
		id = d.Default()
	}
	// Grounding resolves a question against a semantic model; an unmodelled
	// database has none, so there is nothing to index and nothing to ground.
	if !eng.Governed() {
		return eng, nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if g, ok := d.grounders[id]; ok {
		return eng, g, nil
	}
	g, gerr := grounding.New(ctx, eng.Model, filepath.Join(d.indexDir, id+".idx.db"))
	if gerr != nil {
		return eng, nil, nil
	}
	if d.exemplars != "" {
		if bank, err := grounding.LoadExemplars(ctx, d.exemplars); err == nil {
			g.WithExemplars(bank)
		}
	}
	d.grounders[id] = g
	return eng, g, nil
}

// Close shuts grounders and engines down.
func (d *Databases) Close() error {
	d.mu.Lock()
	for id, g := range d.grounders {
		_ = g.Close()
		delete(d.grounders, id)
	}
	d.mu.Unlock()
	return d.Registry.Close()
}

// DatabaseFromRequest decides which database a request is for.
//
// Selection is deliberately out of the model's reach. Over MCP it comes from
// the URL the product connected to (/db/{id}); over REST from a header or query
// parameter the product sets. A tool argument would have worked too, and would
// have let a model wander between a company's databases mid-conversation —
// which database to look at is the product's decision, made once, in the UI.
func DatabaseFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id := PathDatabase(r.URL.Path); id != "" {
		return id
	}
	if id := r.Header.Get("X-DI-Database"); id != "" {
		return id
	}
	return r.URL.Query().Get("database")
}

// PathDatabase extracts {id} from a /db/{id}... path, else "".
func PathDatabase(path string) string {
	const p = "/db/"
	i := strings.Index(path, p)
	if i < 0 {
		return ""
	}
	rest := path[i+len(p):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	if !validID(rest) {
		return ""
	}
	return rest
}

// MountPath is the MCP endpoint for a database, as printed at startup and as
// written into a client's endpoint config.
func MountPath(id string) string { return fmt.Sprintf("/db/%s", id) }
