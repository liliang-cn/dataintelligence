// Package cache is a result cache keyed by (caller, semantic query). Because the
// compiler is deterministic, the same query yields byte-identical SQL, so this
// layer cache (and the warehouse's own result cache) hit cleanly. On a warehouse
// error it serves a labeled STALE answer instead of inventing one — graceful
// degradation. The key includes the caller so governed
// (RBAC/masked/row-scoped) results are never served across identities.
package cache

import (
	"encoding/json"
	"sync"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
)

// Result wraps an answer with cache metadata.
type Result struct {
	Ans   *engine.Answer
	Hit   bool
	Stale bool  // served from an expired entry after a fresh fetch failed
	AgeMs int64 // age of the served entry (0 for a fresh miss)
}

type item struct {
	ans *engine.Answer
	at  int64 // unix nanos
}

// Cache is an in-memory TTL result cache (use one per long-running process).
type Cache struct {
	mu     sync.Mutex
	ttlNs  int64
	m      map[string]item
	hits   int
	misses int
	now    func() int64
}

func New(ttl time.Duration) *Cache {
	return &Cache{ttlNs: int64(ttl), m: map[string]item{}, now: func() int64 { return time.Now().UnixNano() }}
}

// Key is the stable cache key for a governed query: caller + the query shape.
func Key(role string, attrs map[string]string, q semantic.Query) string {
	b, _ := json.Marshal(struct {
		Role  string
		Attrs map[string]string
		Q     semantic.Query
	}{role, attrs, q})
	return string(b)
}

// Query returns a cached fresh answer, or calls fresh() and caches it. If fresh()
// fails but a (possibly expired) entry exists, that entry is returned as Stale.
func (c *Cache) Query(key string, fresh func() (*engine.Answer, error)) (*Result, error) {
	now := c.now()
	c.mu.Lock()
	it, ok := c.m[key]
	c.mu.Unlock()

	if ok && now-it.at <= c.ttlNs {
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()
		return &Result{Ans: it.ans, Hit: true, AgeMs: (now - it.at) / 1e6}, nil
	}

	ans, err := fresh()
	if err != nil {
		if ok { // graceful degradation: serve the stale entry, clearly labeled
			return &Result{Ans: it.ans, Stale: true, AgeMs: (now - it.at) / 1e6}, nil
		}
		return nil, err // no cache to fall back on — honest failure, never invented
	}
	c.mu.Lock()
	c.m[key] = item{ans: ans, at: now}
	c.misses++
	c.mu.Unlock()
	return &Result{Ans: ans}, nil
}

// Invalidate drops a key (e.g. after upstream data changes); empty drops all.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == "" {
		c.m = map[string]item{}
		return
	}
	delete(c.m, key)
}

// Stats returns hits, misses, and current entry count.
func (c *Cache) Stats() (hits, misses, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, len(c.m)
}
