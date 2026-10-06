package copilot

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"

	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/remotemcp"
)

const model = `
entities:
  - {name: run,  table: runs,  primary_key: run_id}
  - {name: line, table: lines, primary_key: line_id}
joins:
  - {from: run, to: line, cardinality: many_to_one, from_key: line_id, to_key: line_id}
dimensions:
  - {name: line_name, entity: line, column: name, type: categorical}
  - {name: run_date,  entity: run,  column: day,  type: time}
metrics:
  - {name: output, description: units made, entity: run, agg: sum, expr: qty}
`

func fixture(t *testing.T) (*engine.Engine, *consult.Service) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "wh.db")
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE lines (line_id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE runs (run_id INTEGER PRIMARY KEY, line_id INTEGER, day TEXT, qty REAL)`,
		`INSERT INTO lines VALUES (1,'A'),(2,'B')`,
	}
	day := time.Now().UTC().AddDate(0, 0, -40)
	for i := 0; i < 40; i++ {
		d := day.AddDate(0, 0, i).Format("2006-01-02")
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO runs VALUES (%d,1,'%s',10)`, 2*i+1, d),
			fmt.Sprintf(`INSERT INTO runs VALUES (%d,2,'%s',5)`, 2*i+2, d))
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(model), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(context.Background(), mp, "sqlite://"+db+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	st, err := consult.OpenStore(filepath.Join(dir, "c.db"), "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := &consult.Service{Store: st, Model: eng.Model, Dialect: eng.Dialect,
		Query: consult.Governed(eng, governance.Policy{}), MeasureAs: governance.Principal{User: "consult", Role: "analyst"}}
	return eng, svc
}

func remote(t *testing.T) *remotemcp.Set {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "plant", Version: "0"}, nil)
	for _, n := range []string{"list_sites", "send_command"} {
		mcp.AddTool(srv, &mcp.Tool{Name: n, Description: n + " tool"},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: n + " ok"}}}, nil, nil
			})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(ts.Close)
	s, err := remotemcp.New([]remotemcp.Server{{Name: "plant", URL: ts.URL, Tools: []string{"list_sites"}, Actions: []string{"send_command"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func byName(ts []tool, name string) *tool {
	for i := range ts {
		if ts[i].name == name {
			return &ts[i]
		}
	}
	return nil
}

// The agent's toolset: governed reads, allow-listed external reads, and the
// consult tools that record — never one that adopts, executes or accepts, and
// never an external action.
func TestTheAgentCannotDecideOrAct(t *testing.T) {
	eng, svc := fixture(t)
	ts := buildTools(context.Background(), eng, governance.Policy{}, Options{Consult: svc, Remote: remote(t), Principal: governance.Principal{User: "copilot", Role: "analyst"}})
	var names []string
	for _, x := range ts {
		names = append(names, x.name)
	}
	for _, want := range []string{"query_metric", "plant__list_sites", "consult_list", "consult_add_goal", "consult_add_finding", "consult_propose_plan"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s in %v", want, names)
		}
	}
	for _, n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "send_command") || strings.Contains(l, "adopt") || strings.Contains(l, "accept") || strings.Contains(l, "execute") {
			t.Errorf("the agent must not have %s", n)
		}
	}
	if byName(ts, "health_check") != nil {
		t.Error("health_check needs a checks file; without one it must not be offered")
	}
	if d := byName(ts, "consult_propose_plan").desc; !strings.Contains(d, `tool="send_command"`) {
		t.Errorf("the plan tool must tell the agent which actions exist: %s", d)
	}
	out, _ := byName(ts, "plant__list_sites").handler(context.Background(), map[string]any{})
	if out != "list_sites ok" {
		t.Errorf("external read = %v", out)
	}
}

func TestQueryMetricFiltersWindowsAndReturnsSQL(t *testing.T) {
	eng, _ := fixture(t)
	ts := buildTools(context.Background(), eng, governance.Policy{}, Options{Principal: governance.Principal{User: "u", Role: "analyst"}})
	res, err := byName(ts, "query_metric").handler(context.Background(), map[string]any{
		"metrics": []any{"output"},
		"filters": []any{map[string]any{"dimension": "line_name", "op": "=", "values": []any{"A"}}},
		"time":    map[string]any{"last_days": float64(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["error"] != nil {
		t.Fatal(m["error"])
	}
	rows := m["rows"].([][]any)
	// Line A makes 10/day; the fixture's last row is yesterday, so the last 10
	// days including today hold 9 of them.
	if got := fmt.Sprint(rows[0][0]); got != "90" {
		t.Errorf("output = %s, want 90 (sql %s)", got, m["sql"])
	}
	if !strings.Contains(m["sql"].(string), "WHERE") {
		t.Errorf("sql = %s", m["sql"])
	}
}

// Identity comes from the caller, never from the model: the plan's proposer is
// the person on whose behalf the copilot ran.
func TestConsultToolsRecordTheCallerAsProposer(t *testing.T) {
	eng, svc := fixture(t)
	ts := buildTools(context.Background(), eng, governance.Policy{}, Options{Consult: svc, Principal: governance.Principal{User: "copilot", Role: "analyst"}})
	ctx := WithPrincipal(context.Background(), governance.Principal{User: "alice", Role: "analyst"})
	g, _ := byName(ts, "consult_add_goal").handler(ctx, map[string]any{
		"what": "A 线产量提一成", "metric": "output", "direction": "up", "by": 0.1, "within_days": float64(14),
		"scope": []any{map[string]any{"dimension": "line_name", "op": "=", "values": []any{"A"}}},
	})
	gid, _ := g.(map[string]any)["goal"].(string)
	if gid == "" {
		t.Fatalf("goal = %v", g)
	}
	// A finding without evidence is refused, and the model is told why.
	r, _ := byName(ts, "consult_add_finding").handler(ctx, map[string]any{"goal": gid, "says": "x"})
	if r.(map[string]any)["refused"] != string(consult.RuleNoEvidence) {
		t.Fatalf("finding without evidence = %v", r)
	}
	f, _ := byName(ts, "consult_add_finding").handler(ctx, map[string]any{"goal": gid, "says": "A 线产量是 B 线两倍",
		"evidence": []any{map[string]any{"asked": "按线", "query": map[string]any{"metrics": []any{"output"}, "group_by": []any{"line_name"}}}}})
	fid, _ := f.(map[string]any)["finding"].(string)
	if fid == "" {
		t.Fatalf("finding = %v", f)
	}
	p, _ := byName(ts, "consult_propose_plan").handler(ctx, map[string]any{"goal": gid, "findings": []any{fid}, "does": "加一班",
		"expect": map[string]any{"metric": "output", "direction": "up", "by": 0.1, "within_days": float64(14)}})
	pid, _ := p.(map[string]any)["plan"].(string)
	if pid == "" {
		t.Fatalf("plan = %v", p)
	}
	plan, err := svc.Store.Plan(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if plan.By != "alice" || plan.Via != "copilot" || len(plan.Expect.Scope) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	_, err = svc.Adopt(context.Background(), pid, governance.Principal{User: "alice"}, "", "")
	if consult.RuleOf(err) != consult.RuleSelfApproval {
		t.Fatalf("the person who asked the copilot cannot adopt its plan: %v", err)
	}
}

func TestSystemPromptRules(t *testing.T) {
	p := SystemPrompt("车间简报")
	for _, want := range []string{"Never invent numbers", "must come from a tool result", "proposed plans", "车间简报"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
