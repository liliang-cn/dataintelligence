package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example that ships with the repository must load under the strict
// decoder — it is the file people copy, and a key it carries that the struct
// does not know would now stop the service rather than be ignored.
func TestTheShippedExampleConfigLoads(t *testing.T) {
	t.Setenv("DI_DSN", "postgres://u:p@localhost:5432/db")
	for _, k := range []string{"DI_OIDC_ISSUER", "DI_OIDC_AUDIENCE", "DI_OIDC_JWKS_URL"} {
		t.Setenv(k, "x")
	}
	if _, err := Load("config.example.yaml"); err != nil {
		t.Fatalf("config.example.yaml no longer loads: %v", err)
	}
}

// An unknown key is an error that names the key. The lenient decoder accepted
// `server: {addr: ...}` — no such field — and the service quietly listened on
// its default ports instead.
func TestAnUnknownKeyIsRefusedByName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	body := "warehouse:\n  dsn: postgres://u:p@h/db\nserver:\n  addr: \":38417\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("a config with a key that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "addr") {
		t.Errorf("the error does not name the unknown key: %v", err)
	}
}
