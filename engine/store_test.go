package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "databases.json")
	s := NewStore(path)

	// Nothing saved yet is the normal state, not an error.
	got, err := s.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("Load of a missing file = %v, %v", got, err)
	}

	defs := []Def{
		{ID: "shop", DSN: "mysql://u:p@h:3306/shop"},
		{ID: "alpha", Model: "a.yaml", DSN: "postgres://a", AllowRawSQL: true},
	}
	if err := s.Save(defs); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Sorted on save so the file does not churn between writes.
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "shop" {
		t.Fatalf("round trip = %+v", got)
	}
	if !got[0].AllowRawSQL || got[0].Model != "a.yaml" {
		t.Errorf("fields lost in the round trip: %+v", got[0])
	}

	// The file holds DSNs — credentials — so it must not be world-readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600 (the file contains credentials)", perm)
	}
}

// A corrupt file must be reported. Treating it as empty would drop a customer's
// saved connections without anyone noticing.
func TestStoreReportsACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "databases.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(); err == nil {
		t.Error("a corrupt file should be reported, not silently treated as empty")
	}
}

func TestDisabledStoreIsANoOp(t *testing.T) {
	s := NewStore("")
	if s.Enabled() {
		t.Error("an empty path should disable persistence")
	}
	if err := s.Save([]Def{{ID: "x", DSN: "y"}}); err != nil {
		t.Errorf("Save on a disabled store should be a no-op, got %v", err)
	}
}
