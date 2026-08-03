package engagement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Paths resolve against the file, not the working directory: a model path that
// only works from one directory turns a reproducible delivery into a personal one.
func TestPathsResolveAgainstTheEngagementFile(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "engagement.yaml", `
customer: Acme
databases:
  - id: erp
    dsn: sqlite:///tmp/erp.db
    model: models/erp.yaml
evalset: models/questions.yaml
deliver:
  report: out/delivery.md
`)
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Databases[0].Model; got != filepath.Join(dir, "models/erp.yaml") {
		t.Errorf("model = %q, want it under the engagement dir", got)
	}
	// The reconciliation set defaults beside the model rather than needing to
	// be spelled out in every engagement.
	if got := e.Databases[0].Recon; got != filepath.Join(dir, "models/erp.recon.yaml") {
		t.Errorf("recon = %q, want models/erp.recon.yaml", got)
	}
	if got := e.Evalset; got != filepath.Join(dir, "models/questions.yaml") {
		t.Errorf("evalset = %q", got)
	}
}

// An unset ${ERP_DSN} expands to "" and then surfaces as "needs a dsn" — true,
// unhelpful, and pointing at the file rather than the environment. Name the
// variable instead.
func TestMissingEnvVariablesAreNamed(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "engagement.yaml", `
customer: Acme
databases:
  - id: erp
    dsn: ${DI_TEST_NOT_SET_ERP}
  - id: pos
    dsn: ${DI_TEST_NOT_SET_POS}
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"not set", "DI_TEST_NOT_SET_ERP", "DI_TEST_NOT_SET_POS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestExpandsEnvSoCredentialsStayOutOfTheFile(t *testing.T) {
	t.Setenv("DI_TEST_ERP_DSN", "postgres://u:p@h:5432/erp")
	dir := t.TempDir()
	path := write(t, dir, "engagement.yaml", `
customer: Acme
databases:
  - id: erp
    dsn: ${DI_TEST_ERP_DSN}
`)
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e.Databases[0].DSN != "postgres://u:p@h:5432/erp" {
		t.Errorf("dsn = %q", e.Databases[0].DSN)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no customer":  "databases:\n  - id: a\n    dsn: sqlite://x\n",
		"no databases": "customer: Acme\n",
		"no id":        "customer: Acme\ndatabases:\n  - dsn: sqlite://x\n",
		"no dsn":       "customer: Acme\ndatabases:\n  - id: a\n",
		"duplicate id": "customer: Acme\ndatabases:\n  - id: a\n    dsn: sqlite://x\n  - id: a\n    dsn: sqlite://y\n",
	} {
		p := write(t, filepath.Join(dir, strings.ReplaceAll(name, " ", "_")), "engagement.yaml", body)
		if _, err := Load(p); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

// An unknown database name and a database that is down look the same to a
// caller unless the error says what was declared.
func TestUnknownDatabaseListsWhatWasDeclared(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "engagement.yaml", `
customer: Acme
databases:
  - id: erp
    dsn: sqlite://a
  - id: pos
    dsn: sqlite://b
`)
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := e.Database(""); err != nil || d.ID != "erp" {
		t.Errorf(`Database("") should be the first, got %v %v`, d, err)
	}
	_, err = e.Database("crm")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"crm", "erp", "pos", "Acme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

// Week one has databases nobody has modelled yet. The file has to be able to
// say so instead of pretending.
func TestModelledCountsProgress(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "engagement.yaml", `
customer: Acme
databases:
  - id: erp
    dsn: sqlite://a
    model: m.yaml
  - id: pos
    dsn: sqlite://b
`)
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m, total := e.Modelled(); m != 1 || total != 2 {
		t.Errorf("Modelled() = %d/%d, want 1/2", m, total)
	}
}

// A command run from a subdirectory of the engagement should still know which
// customer it is in.
func TestFindWalksUpward(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "engagement.yaml", "customer: Acme\ndatabases:\n  - id: a\n    dsn: sqlite://x\n")
	deep := filepath.Join(dir, "models", "drafts")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	got, err := Find("")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(dir, "engagement.yaml"))
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Errorf("Find() = %q, want %q", gotResolved, want)
	}
}
