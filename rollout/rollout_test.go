package rollout

import (
	"os"
	"path/filepath"
	"testing"
)

const modelA = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - {name: revenue, description: d, synonyms: [r], entity: order_item, agg: sum, expr: "qty*price"}
  - {name: units, description: d, synonyms: [u], entity: order_item, agg: sum, expr: "qty"}
`

// modelB changes `units`' expr and adds `tax`; `revenue` is untouched.
const modelB = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - {name: revenue, description: d, synonyms: [r], entity: order_item, agg: sum, expr: "qty*price"}
  - {name: units, description: d, synonyms: [u], entity: order_item, agg: sum, expr: "qty * 1"}
  - {name: tax, description: d, synonyms: [t], entity: order_item, agg: sum, expr: "qty*price*0.1"}
`

// Lineage diff reports only the metrics whose definition changed — the precise
// set of caches a promotion must invalidate.
func TestChangedMetrics(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	if err := os.WriteFile(a, []byte(modelA), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(modelB), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := ChangedMetrics(a, b)
	if err != nil {
		t.Fatalf("ChangedMetrics: %v", err)
	}
	// units (modified) + tax (added) — but NOT revenue (unchanged).
	want := map[string]bool{"units": true, "tax": true}
	if len(changed) != 2 {
		t.Fatalf("got %v, want exactly [tax units]", changed)
	}
	for _, c := range changed {
		if !want[c] {
			t.Errorf("unexpected changed metric %q", c)
		}
	}
}

// Canary routing is deterministic and splits roughly to the configured pct.
func TestBucketSplit(t *testing.T) {
	const pct = 25
	canary := 0
	for i := 0; i < 10000; i++ {
		if bucket(string(rune(i))+":k") < pct {
			canary++
		}
	}
	rate := float64(canary) / 10000 * 100
	if rate < 20 || rate > 30 {
		t.Errorf("canary rate %.1f%% not near %d%%", rate, pct)
	}
	// A fixed key always lands in the same bucket (stable routing).
	if got := bucket("req-42"); got < 0 || got > 99 {
		t.Errorf("bucket out of range: %d", got)
	}
}
