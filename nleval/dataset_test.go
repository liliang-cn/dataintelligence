package nleval

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A misspelled key must fail loudly. `expect:` where the field is
// `expect_metrics:` decoded to an empty expectation on every case, every case
// failed, and the acceptance report told a customer their delivery answered 0%
// of their questions correctly. Nothing errored.
func TestMisspelledKeyIsAnErrorNotAnEmptyExpectation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "set.yaml")
	if err := os.WriteFile(path, []byte("cases:\n  - name: a\n    question: q\n    expect: [revenue]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := Load(path)
	if err == nil {
		t.Fatalf("accepted an unknown key; got %+v", ds)
	}
	if !strings.Contains(err.Error(), "expect") {
		t.Errorf("error must name the offending field, got: %v", err)
	}
}

// A missing file stays distinguishable from a broken one: callers skip the
// first and must not skip the second.
func TestMissingSetIsNotExist(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}
