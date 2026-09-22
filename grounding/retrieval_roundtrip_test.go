package grounding

import (
	"context"
	"path/filepath"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

// The one seam between this repo and cortexdb is three calls: Open,
// Vector().Upsert and Quick().SearchTextOnly. Nothing else in the package
// touches the brain, and until now nothing exercised it — grounding's only
// test was about tokens, so `go test ./...` stayed green across a store
// upgrade that could have changed the on-disk format under it.
//
// Compiling is not the property that matters here. What matters is that a
// metric indexed through Upsert comes back out of SearchTextOnly, by its
// synonym rather than its name, because that is the whole reason the
// description and synonyms are written into the document in the first place.
func TestAMetricIndexedInTheBrainComesBackByItsSynonym(t *testing.T) {
	model := &semantic.Model{
		Metrics: []semantic.Metric{
			{Name: "revenue", Description: "money booked", Synonyms: []string{"top line"}},
			{Name: "headcount", Description: "people employed", Synonyms: []string{"staff"}},
		},
	}
	g, err := New(context.Background(), model, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	defer g.Close()

	hits, err := g.Retrieve(context.Background(), "how is our top line doing")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the brain returned nothing for a question naming a synonym of an indexed metric")
	}
	if hits[0].Name != "revenue" {
		t.Errorf("top hit = %q, want revenue; full order %v", hits[0].Name, hits)
	}
}
