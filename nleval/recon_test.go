package nleval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReconPathFollowsTheModel(t *testing.T) {
	for in, want := range map[string]string{
		"models/shop.yaml":   "models/shop.recon.yaml",
		"/abs/meridian.yaml": "/abs/meridian.recon.yaml",
		"m.yml":              "m.recon.yml",
	} {
		if got := ReconPathFor(in); got != want {
			t.Errorf("ReconPathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// A case without a control query is not a check. Accepting it would let a
// reconciliation set pass while verifying nothing.
func TestLoadRejectsACaseWithNoControlQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.recon.yaml")
	if err := os.WriteFile(path, []byte("cases:\n  - metric: revenue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReconSet(path); err == nil {
		t.Error("a case with no control query should be rejected")
	}

	if err := os.WriteFile(path, []byte("cases:\n  - metric: revenue\n    control: SELECT 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadReconSet(path)
	if err != nil || len(set.Cases) != 1 {
		t.Fatalf("valid set = %v, %v", set, err)
	}
}

func TestCloseEnoughUsesRelativeTolerance(t *testing.T) {
	// Floating-point revenue will not compare exactly; a fixed epsilon would
	// either miss a real drift at large scale or reject noise at small scale.
	if !closeEnough(6178743.62, 6178743.6200001, 1e-6) {
		t.Error("float noise at scale should pass")
	}
	if closeEnough(36210, 36211, 1e-6) {
		t.Error("a whole unit of drift should fail")
	}
	if !closeEnough(0, 0, 1e-6) {
		t.Error("zero equals zero")
	}
}
