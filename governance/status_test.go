package governance

import (
	"context"
	"strings"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"
)

// Every error a governed query returned became 403 at the REST layer: a metric
// that does not exist read as "forbidden", and so did a warehouse that was
// down. The kind of no is now carried with the error, and the message is not
// touched — the pentest and every caller that shows it read the text.
func TestTheStatusSaysWhichKindOfNoItIs(t *testing.T) {
	eng := gatedEngine(t)
	ctx := context.Background()
	pol := DefaultPolicy()

	cases := []struct {
		name   string
		q      semantic.Query
		role   string
		status int
		msg    string
	}{
		{"a metric the role may not read", semantic.Query{Metrics: []string{"revenue"}}, "analyst", 403, "not authorized"},
		{"a metric that does not exist", semantic.Query{Metrics: []string{"no_such_metric"}}, "finance", 400, "no_such_metric"},
		{"a dimension that does not exist", semantic.Query{Metrics: []string{"units"}, GroupBy: []string{"no_such_dim"}}, "finance", 400, "no_such_dim"},
	}
	for _, c := range cases {
		_, err := Query(ctx, eng, c.q, Principal{User: "t", Role: c.role}, pol)
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if got := HTTPStatus(err); got != c.status {
			t.Errorf("%s: status %d, want %d (%v)", c.name, got, c.status, err)
		}
		if !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: the message changed: %v", c.name, err)
		}
	}

	// The warehouse failing is neither the caller's fault nor a refusal.
	if _, err := eng.WH.Exec(ctx, `DROP TABLE order_line`); err != nil {
		t.Fatal(err)
	}
	_, err := Query(ctx, eng, semantic.Query{Metrics: []string{"units"}}, Principal{User: "t", Role: "finance"}, pol)
	if err == nil {
		t.Fatal("a query against a dropped table succeeded")
	}
	if got := HTTPStatus(err); got != 502 {
		t.Errorf("a warehouse failure is %d, want 502: %v", got, err)
	}
}
