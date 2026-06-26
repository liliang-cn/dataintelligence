// Package rollout is the production change-management plane (M21): a persisted
// model-version registry, deterministic canary traffic-splitting, and
// lineage-driven cache invalidation. A model change is never flipped on for
// everyone at once — it is registered, canaried to a slice of traffic, and
// promoted only when it holds; on promotion only the caches whose lineage
// actually changed are dropped. It is domain-neutral: it versions whatever
// semantic model it is pointed at.
package rollout

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Version lifecycle.
const (
	StatusCandidate = "candidate" // registered, not serving
	StatusCanary    = "canary"    // serving a slice of traffic
	StatusActive    = "active"    // serving the rest (the current default)
	StatusRetired   = "retired"   // superseded
)

// Version is one registered model snapshot.
type Version struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Hash      string `json:"hash"`
	Status    string `json:"status"`
	CanaryPct int    `json:"canary_pct"`
	At        string `json:"at"`
}

// Registry persists versions in the warehouse so rollouts survive restarts.
type Registry struct {
	wh *warehouse.Warehouse
	// nowStr is injected so the package stays deterministic-friendly.
	nowStr func() string
}

func New(wh *warehouse.Warehouse, nowStr func() string) *Registry {
	return &Registry{wh: wh, nowStr: nowStr}
}

func (r *Registry) ensure(ctx context.Context) error {
	_, err := r.wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _model_versions (
		name text PRIMARY KEY, doc jsonb, updated_at timestamptz DEFAULT now())`)
	return err
}

// Register hashes a model file and stores it as a candidate.
func (r *Registry) Register(ctx context.Context, name, path string) (*Version, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// validate it loads as a model before registering
	if _, err := semantic.Load(b); err != nil {
		return nil, fmt.Errorf("invalid model: %w", err)
	}
	v := &Version{Name: name, Path: path, Hash: fmt.Sprintf("%x", sha256.Sum256(b))[:12], Status: StatusCandidate, At: r.nowStr()}
	return v, r.save(ctx, v)
}

// Canary marks a candidate as serving pct% of traffic. Only one version may be
// canary at a time, so any existing canary is demoted back to candidate first.
func (r *Registry) Canary(ctx context.Context, name string, pct int) (*Version, error) {
	if pct < 0 || pct > 100 {
		return nil, fmt.Errorf("canary pct must be 0..100")
	}
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.Status == StatusCanary && v.Name != name {
			v.Status = StatusCandidate
			v.CanaryPct = 0
			if err := r.save(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	v, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	v.Status = StatusCanary
	v.CanaryPct = pct
	return v, r.save(ctx, v)
}

// Promote makes name the active version, retires the previous active, and
// returns the lineage delta (metrics whose definition changed) so the caller
// can invalidate exactly those caches — not the whole cache.
func (r *Registry) Promote(ctx context.Context, name string) (changed []string, err error) {
	cand, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	prev, _ := r.Active(ctx) // may be nil on first promotion
	if prev != nil {
		changed, err = ChangedMetrics(prev.Path, cand.Path)
		if err != nil {
			return nil, err
		}
		prev.Status = StatusRetired
		if err := r.save(ctx, prev); err != nil {
			return nil, err
		}
	}
	cand.Status = StatusActive
	cand.CanaryPct = 0
	return changed, r.save(ctx, cand)
}

// Rollback is the panic button: demote the current canary back to candidate,
// and if there is no active version left, restore the most recently retired one.
// Returns the version that ends up active (nil if the active was untouched).
func (r *Registry) Rollback(ctx context.Context) (*Version, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.Status == StatusCanary {
			v.Status = StatusCandidate
			v.CanaryPct = 0
			if err := r.save(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	// If an active version still stands, the demotion is enough.
	if active, err := r.Active(ctx); err == nil {
		return active, nil
	}
	// Otherwise promote the newest retired version back to active.
	for _, v := range all {
		if v.Status == StatusRetired {
			v.Status = StatusActive
			return v, r.save(ctx, v)
		}
	}
	return nil, nil
}

// Route picks which version serves a request, deterministically by the request
// key so the same caller/query is stable across calls. Returns the canary when
// hash(key) falls in the canary's percentage band, else the active version.
func (r *Registry) Route(ctx context.Context, key string) (*Version, error) {
	active, _ := r.Active(ctx)
	canary, _ := r.CanaryVersion(ctx)
	if canary != nil && bucket(key) < canary.CanaryPct {
		return canary, nil
	}
	if active == nil {
		return nil, fmt.Errorf("no active version")
	}
	return active, nil
}

// bucket maps a key to 0..99 deterministically.
func bucket(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % 100)
}

func (r *Registry) Active(ctx context.Context) (*Version, error) { return r.byStatus(ctx, StatusActive) }

// CanaryVersion returns the version currently serving canary traffic.
func (r *Registry) CanaryVersion(ctx context.Context) (*Version, error) {
	return r.byStatus(ctx, StatusCanary)
}

func (r *Registry) byStatus(ctx context.Context, status string) (*Version, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.Status == status {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no %s version", status)
}

func (r *Registry) Get(ctx context.Context, name string) (*Version, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	res, err := r.wh.Query(ctx, `SELECT doc FROM _model_versions WHERE name=$1`, name)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("version %q not registered", name)
	}
	return decode(res.Rows[0][0])
}

func (r *Registry) List(ctx context.Context) ([]*Version, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	res, err := r.wh.Query(ctx, `SELECT doc FROM _model_versions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	var out []*Version
	for _, row := range res.Rows {
		v, err := decode(row[0])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *Registry) save(ctx context.Context, v *Version) error {
	doc, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = r.wh.Exec(ctx, `INSERT INTO _model_versions (name, doc, updated_at) VALUES ($1,$2, now())
		ON CONFLICT (name) DO UPDATE SET doc=$2, updated_at=now()`, v.Name, string(doc))
	return err
}

func decode(val any) (*Version, error) {
	var b []byte
	switch t := val.(type) {
	case []byte:
		b = t
	case string:
		b = []byte(t)
	default:
		return nil, fmt.Errorf("unexpected doc type %T", val)
	}
	var v Version
	return &v, json.Unmarshal(b, &v)
}

// ChangedMetrics is the lineage diff between two model files: the names of
// metrics that were added, removed, or whose definition changed. Promoting a
// new model only needs to invalidate caches for THESE metrics.
func ChangedMetrics(pathA, pathB string) ([]string, error) {
	ma, err := semantic.LoadFile(pathA)
	if err != nil {
		return nil, err
	}
	mb, err := semantic.LoadFile(pathB)
	if err != nil {
		return nil, err
	}
	sig := func(m *semantic.Model) map[string]string {
		out := map[string]string{}
		for i := range m.Metrics {
			b, _ := json.Marshal(m.Metrics[i])
			out[m.Metrics[i].Name] = string(b)
		}
		return out
	}
	sa, sb := sig(ma), sig(mb)
	changed := map[string]bool{}
	for name, def := range sb {
		if sa[name] != def {
			changed[name] = true // added or modified
		}
	}
	for name := range sa {
		if _, ok := sb[name]; !ok {
			changed[name] = true // removed
		}
	}
	out := make([]string, 0, len(changed))
	for n := range changed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
