package handover

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

const provModel = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions: []
metrics:
  - {name: revenue, description: d, synonyms: [r], entity: order_item, agg: sum, expr: "qty*price"}
`

func provWorld(t *testing.T) (*engine.Engine, *rollout.Registry, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	whPath := filepath.Join(dir, "wh.db")

	wh, err := warehouse.OpenSQLite(ctx, whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE order_items (id INTEGER PRIMARY KEY, qty INTEGER, price REAL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := wh.Exec(ctx, `CREATE TABLE _audit (ts TEXT, "user" TEXT, role TEXT, metrics TEXT,
		group_by TEXT, "sql" TEXT, refused INTEGER, note TEXT, question TEXT, engagement TEXT, model_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = wh.Close()

	modelPath := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(modelPath, []byte(provModel), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(ctx, modelPath, "sqlite://"+whPath+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	reg := rollout.New(eng.WH, func() string { return "2026-09-22T05:00:00Z" })
	return eng, reg, modelPath
}

func answers(t *testing.T, eng *engine.Engine, hash string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := eng.WH.Exec(context.Background(),
			`INSERT INTO _audit (ts, "user", metrics, model_hash) VALUES (?,?,?,?)`,
			"2026-09-22T05:00:00Z", "someone", "[revenue]", hash); err != nil {
			t.Fatal(err)
		}
	}
}

// A deployment that never turned the registry on must be told so plainly,
// rather than shown a report that reads like everything is accounted for.
func TestADeploymentWithNoRegistryIsToldEveryAnswerIsUnaccounted(t *testing.T) {
	eng, _, modelPath := provWorld(t)
	hash, _ := rollout.HashFile(modelPath)
	answers(t, eng, hash, 7)

	p, err := Attest(context.Background(), eng, "faw", "faw")
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if p.Answers != 7 || p.Approved != 0 || p.Registry {
		t.Fatalf("got %+v, want 7 answers none approved", p)
	}
	var b strings.Builder
	p.WriteMarkdown(&b)
	out := b.String()
	if !strings.Contains(out, "none of them from an approved definition") {
		t.Errorf("the report does not say the answers are unaccounted:\n%s", out)
	}
	if !strings.Contains(out, "di rollout sign") {
		t.Errorf("the report does not say how to fix it:\n%s", out)
	}
}

// With the registry in use, the report separates the approved answers from the
// ones that came from a model nobody promoted — and names the busiest of those
// first, because the size of the exposure is the point.
func TestTheReportSeparatesApprovedAnswersAndLeadsWithTheWorstGap(t *testing.T) {
	eng, reg, modelPath := provWorld(t)
	ctx := context.Background()
	if _, err := reg.Register(ctx, "v1", modelPath); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Sign(ctx, "v1", "", "张三", "口径复核"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	signed, _ := rollout.HashFile(modelPath)
	answers(t, eng, signed, 2)
	answers(t, eng, "deadbeef0000", 9) // a model that never went through the registry

	p, err := Attest(ctx, eng, "faw", "faw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Answers != 11 || p.Approved != 2 || p.Unapproved() != 9 {
		t.Fatalf("got %+v, want 11 answers / 2 approved / 9 not", p)
	}
	if !p.Registry {
		t.Error("the registry was used but the report says it was not")
	}
	if p.Models[0].Hash != "deadbeef0000" {
		t.Errorf("the report leads with %q, want the unapproved model", p.Models[0].Hash)
	}

	var b strings.Builder
	p.WriteMarkdown(&b)
	out := b.String()
	for _, want := range []string{"2 of 11", "**9 came from a definition nobody has approved.**", "张三", "口径复核", "**nobody**"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
	if s := p.Summary(); !strings.Contains(s, "9 of 11") {
		t.Errorf("summary = %q", s)
	}
}
