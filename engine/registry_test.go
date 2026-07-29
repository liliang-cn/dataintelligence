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

func TestEmptyRegistryIsAValidStartingState(t *testing.T) {
	// A product ships DI before its user has connected anything.
	r, err := NewRegistry()
	if err != nil {
		t.Fatalf("an empty registry should be allowed: %v", err)
	}
	if r.Default() != "" || len(r.IDs()) != 0 {
		t.Errorf("empty registry should have no default and no ids")
	}
	_, err = r.Get(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no database is configured") {
		t.Errorf("error should say nothing is configured yet, got %v", err)
	}

	if err := r.Add(Def{ID: "first", DSN: "sqlite:///tmp/x.db"}); err != nil {
		t.Fatal(err)
	}
	if r.Default() != "first" {
		t.Errorf("the first registered database should become the default, got %q", r.Default())
	}
}

// Removing the database that unqualified requests fall back to would silently
// repoint them at a different company's data.
func TestRemovingTheDefaultIsRefusedWhileOthersExist(t *testing.T) {
	r, _ := NewRegistry(
		Def{ID: "a", DSN: "sqlite:///tmp/a.db"},
		Def{ID: "b", DSN: "sqlite:///tmp/b.db"},
	)
	if err := r.Remove("a"); err == nil {
		t.Error("removing the default should be refused while another database exists")
	}
	if err := r.Remove("b"); err != nil {
		t.Errorf("removing a non-default should work: %v", err)
	}
	// Last one standing: removing it just empties the registry, which is valid.
	if err := r.Remove("a"); err != nil {
		t.Errorf("removing the last database should work: %v", err)
	}
}

func TestUnmodelledDatabasesAllowRawSQLAndModelledOnesDoNot(t *testing.T) {
	r, _ := NewRegistry(
		Def{ID: "raw", DSN: "sqlite:///tmp/a.db"},
		Def{ID: "gov", Model: "m.yaml", DSN: "sqlite:///tmp/b.db"},
		Def{ID: "gov_opt", Model: "m.yaml", DSN: "sqlite:///tmp/c.db", AllowRawSQL: true},
	)
	for id, want := range map[string]bool{"raw": true, "gov": false, "gov_opt": true, "nope": false} {
		if got := r.RawSQLAllowed(id); got != want {
			t.Errorf("RawSQLAllowed(%q) = %v, want %v", id, got, want)
		}
	}
}
