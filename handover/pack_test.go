package handover

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/engagement"
)

func writeEngagement(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PACK_TEST_DSN", "postgres://x/y")
	body := `customer: 客户A
owner: liliang
started: 2026-01-01
databases:
  - id: erp
    dsn: ${PACK_TEST_DSN}
    model: models/erp.yaml
`
	if err := os.WriteFile(filepath.Join(dir, "engagement.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range extra {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A handover missing its runbook or its acceptance report is incomplete, and
// the manifest has to say so — discovering it when somebody needs one is the
// expensive way.
func TestPackNamesWhatIsMissing(t *testing.T) {
	dir := writeEngagement(t, "models/erp.yaml")
	e, err := engagement.Load(filepath.Join(dir, "engagement.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(e, filepath.Join(dir, "out.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	md := p.WriteMarkdown()
	for _, want := range []string{"RUNBOOK.md", "out/delivery.md", "out/survey.md"} {
		if !strings.Contains(md, want) {
			t.Errorf("manifest does not mention the missing %s", want)
		}
	}
	if len(p.Files) != 2 { // engagement.yaml + the model
		t.Errorf("packed %d file(s), want 2", len(p.Files))
	}
}

// Credentials are named, never carried: an archive gets emailed.
func TestSecretsAreNamedNotIncluded(t *testing.T) {
	dir := writeEngagement(t, "models/erp.yaml")
	e, _ := engagement.Load(filepath.Join(dir, "engagement.yaml"))
	p, err := Build(e, filepath.Join(dir, "out.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Secrets) != 1 || p.Secrets[0] != "PACK_TEST_DSN" {
		t.Fatalf("secrets = %v, want [PACK_TEST_DSN]", p.Secrets)
	}
	for _, body := range readArchive(t, filepath.Join(dir, "out.tar.gz")) {
		if strings.Contains(body, "postgres://x/y") {
			t.Error("the expanded DSN is inside the archive")
		}
	}
}

// The same inputs must produce the same archive, or the digest of a delivery
// changes every time somebody rebuilds it and stops meaning anything.
func TestArchiveIsReproducible(t *testing.T) {
	dir := writeEngagement(t, "models/erp.yaml", "RUNBOOK.md")
	e, _ := engagement.Load(filepath.Join(dir, "engagement.yaml"))
	a, err := Build(e, filepath.Join(dir, "a.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(e, filepath.Join(dir, "b.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Files) != len(b.Files) {
		t.Fatalf("%d vs %d files", len(a.Files), len(b.Files))
	}
	for i := range a.Files {
		if a.Files[i].SHA256 != b.Files[i].SHA256 {
			t.Errorf("%s: digest differs between builds", a.Files[i].Path)
		}
	}
}

func readArchive(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		b := make([]byte, h.Size)
		_, _ = tr.Read(b)
		out[h.Name] = string(b)
	}
}
