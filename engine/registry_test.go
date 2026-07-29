package engine

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryRejectsIdsThatCannotBeRouted(t *testing.T) {
	// The MCP endpoint for a database is /db/{id}. An id with a slash would be
	// accepted here and then quietly resolve to a different database — or none.
	for _, bad := range []string{"a/b", "with space", "d?x", ""} {
		if _, err := NewRegistry(Def{ID: bad, Model: "m.yaml", DSN: "postgres://x"}); err == nil {
			t.Errorf("id %q should be rejected", bad)
		}
	}
	if _, err := NewRegistry(
		Def{ID: "shop", Model: "m.yaml", DSN: "postgres://x"},
		Def{ID: "shop", Model: "n.yaml", DSN: "mysql://y"},
	); err == nil {
		t.Error("duplicate id should be rejected")
	}
	if _, err := NewRegistry(Def{ID: "shop", Model: "m.yaml"}); err == nil {
		t.Error("a database without a dsn should be rejected")
	}
}

func TestRegistryDefaultsToTheFirstDeclared(t *testing.T) {
	r, err := NewRegistry(
		Def{ID: "alpha", Model: "a.yaml", DSN: "postgres://a"},
		Def{ID: "beta", Model: "b.yaml", DSN: "mysql://b"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.Default() != "alpha" {
		t.Errorf("default = %q, want alpha", r.Default())
	}
	if got := r.IDs(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("IDs = %v, want declaration order", got)
	}
	if !r.Has("beta") || r.Has("gamma") {
		t.Error("Has should reflect configuration without opening anything")
	}
}

// An unknown database must be distinguishable from a warehouse that is down:
// one is a typo the caller can fix, the other is an outage they cannot.
func TestUnknownDatabaseErrorListsWhatExists(t *testing.T) {
	r, err := NewRegistry(
		Def{ID: "alpha", Model: "a.yaml", DSN: "postgres://a"},
		Def{ID: "beta", Model: "b.yaml", DSN: "mysql://b"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Get(context.Background(), "gamma")
	if err == nil {
		t.Fatal("want an error for an unknown database")
	}
	for _, want := range []string{`"gamma"`, "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestPathDatabase(t *testing.T) {
	for path, want := range map[string]string{
		"/db/shop":            "shop",
		"/db/shop_mysql/":     "shop_mysql",
		"/db/shop/extra/bits": "shop",
		"/":                   "",
		"/v1/query":           "",
		"/db/":                "",
		"/db/bad name":        "",
	} {
		if got := PathDatabase(path); got != want {
			t.Errorf("PathDatabase(%q) = %q, want %q", path, got, want)
		}
	}
}
