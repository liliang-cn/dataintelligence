package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

const consultModel = `
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

func consultServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "wh.db")
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE lines (line_id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE runs (run_id INTEGER PRIMARY KEY, line_id INTEGER, day TEXT, qty REAL)`,
		`INSERT INTO lines VALUES (1,'A')`}
	start := time.Now().UTC().AddDate(0, 0, -40)
	for i := 0; i < 40; i++ {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO runs VALUES (%d,1,'%s',10)`, i+1, start.AddDate(0, 0, i).Format("2006-01-02")))
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	mp := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(mp, []byte(consultModel), 0o644); err != nil {
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
	v := &V1{
		Users: []StaticUser{{Name: "alice", Role: "analyst", Token: "tok-a"}, {Name: "bob", Role: "manager", Token: "tok-b"}},
		Consult: &consult.Service{Store: st, Model: eng.Model, Dialect: eng.Dialect,
			Query: consult.Governed(eng, governance.Policy{}), MeasureAs: governance.Principal{User: "consult", Role: "analyst"}},
	}
	ts := httptest.NewServer(v.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestStaticUsersIdentifyCallers(t *testing.T) {
	ts := consultServer(t)
	if code, _ := call(t, ts, "GET", "/v1/whoami", "", nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/v1/whoami", "nope", nil); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	_, me := call(t, ts, "GET", "/v1/whoami", "tok-b", nil)
	if me["user"] != "bob" || me["role"] != "manager" {
		t.Fatalf("whoami = %v", me)
	}
	// The console sends the same token as a cookie.
	req, _ := http.NewRequest("GET", ts.URL+"/v1/whoami", nil)
	req.AddCookie(&http.Cookie{Name: TokenCookie, Value: "tok-a"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j["user"] != "alice" {
		t.Fatalf("cookie identity = %v", j)
	}
	if code, _ := call(t, ts, "GET", "/v1/consult", "", nil); code != 401 {
		t.Fatalf("consult without identity: %d", code)
	}
}

func TestConsultOverHTTP(t *testing.T) {
	ts := consultServer(t)
	scope := []map[string]any{{"dimension": "line_name", "op": "=", "values": []any{"A"}}}
	code, g := call(t, ts, "POST", "/v1/consult/goals", "tok-a", map[string]any{
		"what": "产量提一成", "metric": "output", "direction": "up", "by": 0.1, "within_days": 14, "scope": scope})
	if code != 201 {
		t.Fatalf("goal: %d %v", code, g)
	}
	code, r := call(t, ts, "POST", "/v1/consult/goals", "tok-a", map[string]any{"what": "x", "metric": "nope", "direction": "up", "by": 0.1, "within_days": 14})
	if code != 422 || r["rule"] != string(consult.RuleUnknownMetric) {
		t.Fatalf("unknown metric: %d %v", code, r)
	}
	code, f := call(t, ts, "POST", "/v1/consult/findings", "tok-a", map[string]any{"goal": g["id"], "says": "A 线每天 10",
		"evidence": []any{map[string]any{"asked": "产量", "query": map[string]any{"metrics": []string{"output"}, "group_by": []string{"line_name"}, "time": map[string]any{"last_days": 7}}}}})
	if code != 201 {
		t.Fatalf("finding: %d %v", code, f)
	}
	code, p := call(t, ts, "POST", "/v1/consult/plans", "tok-a", map[string]any{"goal": g["id"], "findings": []any{f["id"]}, "does": "加班",
		"expect": map[string]any{"metric": "output", "direction": "up", "by": 0.1, "within_days": 14}})
	if code != 201 {
		t.Fatalf("plan: %d %v", code, p)
	}
	pid := p["id"].(string)
	code, r = call(t, ts, "POST", "/v1/consult/plans/"+pid+"/adopt", "tok-a", map[string]any{"why": "mine"})
	if code != 422 || r["rule"] != string(consult.RuleSelfApproval) {
		t.Fatalf("self adoption: %d %v", code, r)
	}
	code, r = call(t, ts, "POST", "/v1/consult/plans/"+pid+"/adopt", "tok-b", map[string]any{"why": "go"})
	if code != 200 {
		t.Fatalf("adopt: %d %v", code, r)
	}
	base := r["decision"].(map[string]any)["baseline"].(map[string]any)
	if base["value"].(float64) != 140 {
		t.Fatalf("baseline = %v", base)
	}
	code, r = call(t, ts, "POST", "/v1/consult/plans/"+pid+"/accept", "tok-b", nil)
	if code != 422 || r["rule"] != string(consult.RuleTooEarly) {
		t.Fatalf("early accept: %d %v", code, r)
	}
	code, r = call(t, ts, "POST", "/v1/consult/plans/"+pid+"/measure", "tok-b", nil)
	if code != 200 || r["final"] != false {
		t.Fatalf("measure: %d %v", code, r)
	}
	code, b := call(t, ts, "GET", "/v1/consult", "tok-b", nil)
	goals := b["goals"].([]any)
	plans := goals[0].(map[string]any)["plans"].([]any)
	if code != 200 || plans[0].(map[string]any)["state"] != "adopted" {
		t.Fatalf("board: %d %v", code, b)
	}
	if code, _ := call(t, ts, "GET", "/v1/consult/plans/p9", "tok-b", nil); code != 404 {
		t.Fatalf("missing plan: %d", code)
	}
}
