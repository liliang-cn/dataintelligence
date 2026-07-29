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

// The single-database form must keep working untouched: every existing config
// file and every `di serve -model … -dsn …` depends on it.
func TestDefsNormalisesBothConfigShapes(t *testing.T) {
	single := &Config{Model: "m.yaml", Warehouse: Warehouse{DSN: "postgres://x"}}
	got := single.Defs()
	if len(got) != 1 || got[0].ID != "default" || got[0].Model != "m.yaml" || got[0].DSN != "postgres://x" {
		t.Errorf("single-database form = %+v", got)
	}

	// With an explicit list the top-level pair is ignored rather than merged:
	// merging would make which database is default depend on key order.
	multi := &Config{
		Model:     "ignored.yaml",
		Warehouse: Warehouse{DSN: "postgres://ignored"},
		Databases: []Database{
			{ID: "shop", Model: "shop.yaml", DSN: "mysql://shop"},
			{ID: "wh", Model: "wh.yaml", DSN: "postgres://wh"},
		},
	}
	got = multi.Defs()
	if len(got) != 2 || got[0].ID != "shop" || got[1].ID != "wh" {
		t.Errorf("multi-database form = %+v", got)
	}
}
