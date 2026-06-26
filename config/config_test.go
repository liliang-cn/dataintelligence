package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExpandsEnvAndDefaults(t *testing.T) {
	t.Setenv("DI_TEST_DSN", "postgres://u:p@localhost:5432/db")
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
model: models/meridian.yaml
warehouse:
  dsn: "${DI_TEST_DSN}"
server:
  rest_addr: ":5000"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Warehouse.DSN != "postgres://u:p@localhost:5432/db" {
		t.Errorf("env not expanded: %q", c.Warehouse.DSN)
	}
	if c.Server.RESTAddr != ":5000" {
		t.Errorf("explicit addr lost: %q", c.Server.RESTAddr)
	}
	if c.Server.MCPAddr != ":41910" {
		t.Errorf("MCP addr default not applied: %q", c.Server.MCPAddr)
	}
	if c.Warehouse.TimeoutSecs != 30 || c.Warehouse.MaxRows != 10000 {
		t.Errorf("warehouse defaults not applied: %+v", c.Warehouse)
	}
}

func TestLoadRequiresModelAndDSN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("server:\n  rest_addr: \":5000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when model/dsn missing")
	}
}
