package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const advisorBody = `
model: m.yaml
warehouse: {dsn: "postgres://u:p@h/db"}
auth:
  users:
    - {name: alice, role: analyst, token_env: TOK_A}
    - {name: bob, role: manager, token_env: TOK_B, attrs: {region: South}}
copilot:
  principal: {user: copilot, role: analyst}
  brief_file: brief.md
  mcp:
    - name: plant
      url: "${PLANT_URL}"
      token_env: PLANT_TOKEN
      tools: [list_sites]
      actions: [send_command]
consult:
  time_dimension: day
  measure_as: {user: consult, role: analyst}
  adopt_roles: [manager]
  check_every: 10m
  allow_as_of: true
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdvisorSectionsLoad(t *testing.T) {
	t.Setenv("PLANT_URL", "http://plant:43101/mcp")
	p := writeConfig(t, advisorBody)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Copilot.MCP[0].URL != "http://plant:43101/mcp" || c.Copilot.MCP[0].Actions[0] != "send_command" {
		t.Errorf("mcp = %+v", c.Copilot.MCP)
	}
	if len(c.Auth.Users) != 2 || c.Auth.Users[1].Attrs["region"] != "South" {
		t.Errorf("users = %+v", c.Auth.Users)
	}
	if c.Consult.Interval().Minutes() != 10 || !c.Consult.AllowAsOf {
		t.Errorf("consult = %+v", c.Consult)
	}
	dir := filepath.Dir(p)
	if c.ConsultStore() != filepath.Join(dir, "consult.db") || c.Path(c.Copilot.BriefFile) != filepath.Join(dir, "brief.md") {
		t.Errorf("paths: %s %s", c.ConsultStore(), c.Path(c.Copilot.BriefFile))
	}
}

func TestAModelBesideTheConfigIsFound(t *testing.T) {
	p := writeConfig(t, "model: beside.model.yaml\nwarehouse: {dsn: \"postgres://x\"}\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "beside.model.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != filepath.Join(filepath.Dir(p), "beside.model.yaml") {
		t.Errorf("model = %s", c.Model)
	}
}

func TestAdvisorSectionsAreStrict(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(advisorBody, "actions: [send_command]", "action: [send_command]", 1),
		strings.Replace(advisorBody, "check_every: 10m", "check_every: soon", 1),
		strings.Replace(advisorBody, "token_env: TOK_A", "token: abc", 1),
	} {
		t.Setenv("PLANT_URL", "http://x")
		if _, err := Load(writeConfig(t, bad)); err == nil {
			t.Errorf("accepted a bad advisor config:\n%s", bad)
		}
	}
}

// The SmartFactory example is a file people copy; it must load under the
// strict decoder.
func TestTheSmartFactoryExampleLoads(t *testing.T) {
	t.Setenv("DI_DSN", "postgres://u:p@localhost:5432/db")
	t.Setenv("SF_MCP_URL", "http://localhost:43101/mcp")
	c, err := Load("../examples/smartfactory/config.yaml")
	if err != nil {
		t.Fatalf("examples/smartfactory/config.yaml does not load: %v", err)
	}
	if c.Copilot.MCP[0].Name != "sf" || c.Consult.AdoptRoles[0] != "approver" || len(c.Auth.Users) != 2 {
		t.Errorf("unexpected example: %+v", c)
	}
	if c.Path(c.Copilot.BriefFile) != filepath.Join("..", "examples", "smartfactory", "brief.md") {
		t.Errorf("brief path = %s", c.Path(c.Copilot.BriefFile))
	}
}
