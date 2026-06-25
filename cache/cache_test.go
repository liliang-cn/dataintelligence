package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/dataintelligence/engine"
)

func TestCacheHitAndGracefulDegradation(t *testing.T) {
	c := New(time.Minute)
	now := int64(1000)
	c.now = func() int64 { return now }

	calls := 0
	freshOK := func() (*engine.Answer, error) {
		calls++
		return &engine.Answer{SQL: "v1", Columns: []string{"x"}}, nil
	}

	// miss → fresh
	r, err := c.Query("k", freshOK)
	if err != nil || r.Hit || r.Ans.SQL != "v1" {
		t.Fatalf("miss: hit=%v err=%v", r.Hit, err)
	}
	// within TTL → hit (fresh not called)
	r, err = c.Query("k", freshOK)
	if err != nil || !r.Hit {
		t.Fatalf("expected hit, got hit=%v", r.Hit)
	}
	if calls != 1 {
		t.Fatalf("fresh called %d times, want 1 (2nd served from cache)", calls)
	}

	// advance past TTL, and now the warehouse is "down"
	now += int64(2 * time.Minute)
	r, err = c.Query("k", func() (*engine.Answer, error) { return nil, errors.New("warehouse down") })
	if err != nil {
		t.Fatalf("graceful degradation should not error, got %v", err)
	}
	if !r.Stale || r.Ans.SQL != "v1" {
		t.Fatalf("expected STALE cached answer, got stale=%v ans=%v", r.Stale, r.Ans)
	}

	// no cache + error → honest failure (never invents)
	if _, err := c.Query("missing", func() (*engine.Answer, error) { return nil, errors.New("down") }); err == nil {
		t.Fatal("expected an error when there is no cache to fall back on")
	}
}
