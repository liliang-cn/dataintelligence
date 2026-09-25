package reported

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/corpus"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Two definitions of one number. The first is gross; the second is the one
// somebody re-signed a week later, net of refunds. Same metric name, same
// query, different figure — the case a diff of rows cannot tell from late
// data.
const modelGross = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions:
  - {name: region, entity: order_item, column: region, type: categorical}
metrics:
  - {name: revenue, description: money booked, entity: order_item, agg: sum, expr: "qty*price"}
`

const modelNet = `
entities:
  - {name: order_item, table: order_items, primary_key: id}
dimensions:
  - {name: region, entity: order_item, column: region, type: categorical}
metrics:
  - {name: revenue, description: money booked net of refunds, entity: order_item, agg: sum, expr: "qty*price - refund"}
`

var byRegion = semantic.Query{Metrics: []string{"revenue"}, GroupBy: []string{"region"}}

type world struct {
	ctx      context.Context
	dsn      string
	wh       *warehouse.Warehouse // a writable handle: the "late data" arrives through it
	gross    *engine.Engine
	net      *engine.Engine
	grossYML []byte
	store    *Store
	who      governance.Principal
	policy   governance.Policy
}

// newWorld is a warehouse with three order lines, both definitions
// registered, the gross one signed by 张三 and the net one by 李四, and a
// brain to keep reports in.
func newWorld(t *testing.T) *world {
	t.Helper()
	for _, k := range []string{
		"LLM_BASE_URL", "LLM_BASE", "OPENAI_BASE_URL", "LLM_MODEL", "OPENAI_MODEL",
		"LLM_API_KEY", "LLM_KEY", "OPENAI_API_KEY",
		"DI_EMBED_BASE_URL", "DI_EMBED_API_KEY", "DI_EMBED_MODEL", "DI_DB_APP_ROLE", "DI_MAX_SCAN_BYTES",
	} {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	dir := t.TempDir()
	whPath := filepath.Join(dir, "wh.db")

	wh, err := warehouse.OpenSQLite(ctx, whPath+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wh.Close() })
	if _, err := wh.Exec(ctx, `CREATE TABLE order_items (
		id INTEGER PRIMARY KEY, region TEXT, qty INTEGER, price REAL, refund REAL)`); err != nil {
		t.Fatal(err)
	}
	// north: 2*10 + 3*20 = 80 gross, 75 net. south: 50 either way.
	w := &world{ctx: ctx, wh: wh, dsn: "sqlite://" + whPath + "?mode=rwc"}
	w.insert(t, "north", 2, 10, 0)
	w.insert(t, "north", 3, 20, 5)
	w.insert(t, "south", 1, 50, 0)

	grossPath := filepath.Join(dir, "gross.yaml")
	netPath := filepath.Join(dir, "net.yaml")
	w.grossYML = []byte(modelGross)
	for p, body := range map[string]string{grossPath: modelGross, netPath: modelNet} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.gross = w.engine(t, grossPath)
	w.net = w.engine(t, netPath)

	reg := rollout.New(wh, func() string { return "2026-09-10T03:00:00Z" })
	for _, v := range []struct{ name, path, by, note string }{
		{"v1", grossPath, "张三", "gross, as finance has always booked it"},
		{"v2", netPath, "李四", "net of refunds, per the quality review"},
	} {
		if _, err := reg.Register(ctx, v.name, v.path); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Sign(ctx, v.name, "", v.by, v.note); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Promote(ctx, v.name); err != nil {
			t.Fatal(err)
		}
	}

	bc := cortexdb.DefaultConfig(filepath.Join(dir, "brain.db"))
	bc.Dimensions = corpus.LexicalDim
	brain, err := cortexdb.Open(bc, cortexdb.WithEmbedder(corpus.LexicalEmbedder{}))
	if err != nil {
		t.Fatalf("brain: %v", err)
	}
	t.Cleanup(func() { _ = brain.Close() })
	w.store, err = New(brain, WithRegistry(reg),
		WithClock(func() time.Time { return time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC) }))
	if err != nil {
		t.Fatal(err)
	}
	w.who = governance.Principal{User: "cli", Role: "analyst", Attrs: map[string]string{"region": "north"}}
	w.policy = governance.DefaultPolicy()
	return w
}

func (w *world) engine(t *testing.T, path string) *engine.Engine {
	t.Helper()
	e, err := engine.New(w.ctx, path, w.dsn)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func (w *world) insert(t *testing.T, region string, qty, price, refund float64) {
	t.Helper()
	if _, err := w.wh.Exec(w.ctx, `INSERT INTO order_items (region, qty, price, refund) VALUES (?,?,?,?)`,
		region, qty, price, refund); err != nil {
		t.Fatal(err)
	}
}

// deliver computes a figure under the gross definition and freezes it, the
// way `di brief` followed by "send" would. withModel decides whether the
// definition's bytes go into the record.
func (w *world) deliver(t *testing.T, name string, q semantic.Query, withModel bool) *Report {
	t.Helper()
	ans, err := governance.Query(w.ctx, w.gross, q, w.who, w.policy)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	f := FromAnswer(w.gross, q, ans)
	f.Name, f.By, f.Who, f.Policy = name, "王五", w.who, w.policy
	if withModel {
		f.Model = w.grossYML
	}
	r, err := w.store.Freeze(w.ctx, f)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	return r
}

func (w *world) compare(t *testing.T, name string, e *engine.Engine, pol governance.Policy) *Comparison {
	t.Helper()
	c, err := w.store.Compare(w.ctx, name, Current{Engine: e, Policy: pol})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	return c
}

func causes(c *Comparison) string {
	s := make([]string, len(c.Causes))
	for i, x := range c.Causes {
		s[i] = string(x)
	}
	return strings.Join(s, ",")
}

func row(t *testing.T, rows []RowDiff, key string) RowDiff {
	t.Helper()
	for _, r := range rows {
		if strings.Join(r.Key, "|") == key {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", key, rows)
	return RowDiff{}
}

// Nothing touched: the report and today agree, and the comparison says so in
// a word, rather than printing a table of equal numbers for someone to check.
func TestAReportNothingHasTouchedComparesAsUnchanged(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)
	c := w.compare(t, "0914-revenue", w.gross, w.policy)

	if len(c.Causes) != 0 {
		t.Errorf("causes = %v, want none", c.Causes)
	}
	if c.Frame.DefinitionChanged || c.Frame.AccessChanged {
		t.Errorf("frame reported as moved: %+v", c.Frame)
	}
	if len(c.Rows) != 2 || anyMoved(c.Rows) {
		t.Errorf("rows = %+v, want two, both the same", c.Rows)
	}
	if !strings.HasPrefix(c.Headline(), "UNCHANGED") {
		t.Errorf("headline = %q", c.Headline())
	}
}

// Late data under the definition that was reported is data, names the
// definition's approver as still standing behind it, and never says
// "definition".
func TestLateDataIsAttributedToTheDataAndNotToTheDefinition(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)
	w.insert(t, "south", 2, 5, 0) // south 50 → 60

	c := w.compare(t, "0914-revenue", w.gross, w.policy)
	if got := causes(c); got != "data" {
		t.Fatalf("causes = %q, want data", got)
	}
	if c.Frame.DefinitionChanged {
		t.Error("late data was reported as a change of definition")
	}
	south := row(t, c.Rows, "south")
	if south.Status != Moved || south.Cells[0].Delta == nil || *south.Cells[0].Delta != 10 {
		t.Errorf("south = %+v, want moved by +10", south)
	}
	if north := row(t, c.Rows, "north"); north.Status != Same {
		t.Errorf("north moved: %+v", north)
	}
	h := c.Headline()
	if !strings.HasPrefix(h, "DATA CHANGED") || strings.Contains(h, "DEFINITION") {
		t.Errorf("headline = %q", h)
	}
	if !strings.Contains(h, "张三") {
		t.Errorf("the headline does not name who approved the definition the figure still rests on: %q", h)
	}
}

// A re-signed definition on unchanged data is the definition, names who
// signed the new one, and shows no data movement at all.
func TestAResignedDefinitionIsAttributedToTheDefinitionAndNamesItsApprover(t *testing.T) {
	w := newWorld(t)
	r := w.deliver(t, "0914-revenue", byRegion, true)
	if r.Approval.SignedBy != "张三" {
		t.Fatalf("the report did not record who approved its definition: %+v", r.Approval)
	}

	c := w.compare(t, "0914-revenue", w.net, w.policy)
	if got := causes(c); got != "definition" {
		t.Fatalf("causes = %q, want definition", got)
	}
	if c.Split == nil || !c.Split.Separated {
		t.Fatalf("the movement was not separated: %+v", c.Split)
	}
	if len(c.Split.Data) != 0 {
		t.Errorf("a definition change was partly blamed on data: %+v", c.Split.Data)
	}
	north := row(t, c.Split.Frame, "north")
	if north.Cells[0].Delta == nil || *north.Cells[0].Delta != -5 {
		t.Errorf("north under the new definition = %+v, want -5", north)
	}
	if c.Now.Approval.SignedBy != "李四" {
		t.Errorf("today's approver = %q, want 李四", c.Now.Approval.SignedBy)
	}
	h := c.Headline()
	if !strings.HasPrefix(h, "DEFINITION CHANGED") || strings.Contains(h, "DATA CHANGED") {
		t.Errorf("headline = %q", h)
	}
	for _, name := range []string{"张三", "李四", r.ModelHash, w.net.ModelHash} {
		if !strings.Contains(h, name) {
			t.Errorf("headline does not mention %q: %s", name, h)
		}
	}
}

// Both at once is the hard case, and the reason the old definition is kept:
// replayed on today's data it splits the movement cleanly — south moved
// because rows arrived, north moved because the definition changed.
func TestWhenDataAndDefinitionBothMovedTheReplaySeparatesThem(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)
	w.insert(t, "south", 2, 5, 0)

	c := w.compare(t, "0914-revenue", w.net, w.policy)
	if got := causes(c); got != "data,definition" {
		t.Fatalf("causes = %q, want data,definition", got)
	}
	if len(c.Split.Data) != 1 || strings.Join(c.Split.Data[0].Key, "") != "south" {
		t.Errorf("data movement = %+v, want south only", c.Split.Data)
	}
	if len(c.Split.Frame) != 1 || strings.Join(c.Split.Frame[0].Key, "") != "north" {
		t.Errorf("definition movement = %+v, want north only", c.Split.Frame)
	}
	out := c.Text()
	for _, want := range []string{"moved by the data", "moved by the definition"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering lacks %q:\n%s", want, out)
		}
	}
}

// Without the old definition the split cannot be made, and the comparison
// must not fill the gap with a guess: it names the definition, does not list
// data, and says the question is open.
func TestWithoutTheOldDefinitionADataMovementIsNotClaimedEitherWay(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, false)
	w.insert(t, "south", 2, 5, 0)

	c := w.compare(t, "0914-revenue", w.net, w.policy)
	if got := causes(c); got != "definition" {
		t.Fatalf("causes = %q, want definition alone", got)
	}
	if c.Split == nil || c.Split.Separated {
		t.Fatalf("claimed a separation it had nothing to separate with: %+v", c.Split)
	}
	if !strings.Contains(c.Headline(), "cannot be told") {
		t.Errorf("headline hides that data may also have moved: %q", c.Headline())
	}
}

// A narrower access policy on the same definition and the same data is not
// late data. Same model hash, different figure — without the policy check it
// would be reported as exactly that.
func TestAChangedAccessPolicyIsNotMistakenForLateData(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-total", semantic.Query{Metrics: []string{"revenue"}}, true)

	narrowed := governance.DefaultPolicy()
	narrowed.RowFilters = append(narrowed.RowFilters,
		governance.RowFilter{Dimension: "region", AttrKey: "region", Roles: []string{"analyst"}})
	c := w.compare(t, "0914-total", w.gross, narrowed)
	if got := causes(c); got != "access" {
		t.Fatalf("causes = %q, want access", got)
	}
	if c.Frame.DefinitionChanged {
		t.Error("an access change was reported as a definition change")
	}
	if !strings.HasPrefix(c.Headline(), "ACCESS POLICY CHANGED") {
		t.Errorf("headline = %q", c.Headline())
	}
}

// A region that vanished and one that appeared are both in the comparison,
// each saying which side it is on. Matching by position, or an inner join,
// would drop one and shift the other onto the wrong neighbour.
func TestARowPresentOnlyThenOrOnlyNowIsReportedNotDropped(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)
	if _, err := w.wh.Exec(w.ctx, `DELETE FROM order_items WHERE region = 'south'`); err != nil {
		t.Fatal(err)
	}
	w.insert(t, "east", 1, 7, 0)

	c := w.compare(t, "0914-revenue", w.gross, w.policy)
	if len(c.Rows) != 3 {
		t.Fatalf("rows = %d, want north, south and east: %+v", len(c.Rows), c.Rows)
	}
	south := row(t, c.Rows, "south")
	if south.Status != OnlyThen || toF(south.Cells[0].Then) != 50 || south.Cells[0].Absent != "now" {
		t.Errorf("south = %+v, want only-then at 50", south)
	}
	east := row(t, c.Rows, "east")
	if east.Status != OnlyNow || toF(east.Cells[0].Now) != 7 || east.Cells[0].Absent != "then" {
		t.Errorf("east = %+v, want only-now at 7", east)
	}
	if north := row(t, c.Rows, "north"); north.Status != Same {
		t.Errorf("north = %+v, want same", north)
	}
	if got := causes(c); got != "data" {
		t.Errorf("causes = %q, want data", got)
	}
	out := c.Text()
	for _, want := range []string{"only_then", "only_now"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering lacks %q:\n%s", want, out)
		}
	}
}

func toF(v any) float64 {
	r, ok := number(v)
	if !ok {
		return -1
	}
	f, _ := r.Float64()
	return f
}

// Postgres NUMERIC is text with twenty digits; SQLite is a float64; a decoded
// report is json.Number. The same figure in any of those spellings is the
// same figure, and a figure that really moved — even in the seventh decimal —
// is not.
func TestNumericTextIsComparedAsANumberWithAnExplicitTolerance(t *testing.T) {
	tol := DefaultTolerance
	for _, c := range []struct {
		then, now any
		same      bool
	}{
		{"0.97094126231302444363", 0.9709412623130244, true},
		{"0.97094126231302444363", "0.97094126231302444363", true},
		{json.Number("1.50"), "1.5", true},
		{"122.000000000000000000", int64(122), true},
		{json.Number("0.1"), 0.1, true},
		{"0.97094126231302444363", "0.9709413", false},
		{"80", 80.0000001, false},
		{nil, nil, true},
		{nil, 0.0, false},
		{"长春一厂", "长春一厂", true},
		{"1/3", "0.333333333333", false}, // a label spelled like a fraction is a label
	} {
		if got, _ := tol.same(c.then, c.now); got != c.same {
			t.Errorf("same(%#v, %#v) = %v, want %v", c.then, c.now, got, c.same)
		}
	}
	// Exact comparison is available, and is what it says.
	if eq, _ := (Tolerance{}).same("0.97094126231302444363", 0.9709412623130244); eq {
		t.Error("a zero tolerance treated two different decimals as equal")
	}

	// And through the whole path: a report that arrived from Postgres as
	// NUMERIC text, compared with the same figures from SQLite as floats.
	w := newWorld(t)
	f := FromAnswer(w.gross, byRegion, &engine.Answer{
		Columns: []string{"region", "revenue"},
		Rows:    [][]any{{"north", "80.00000000000000000000"}, {"south", []byte("50.000")}},
	})
	f.Name, f.By, f.Who, f.Policy = "from-postgres", "王五", w.who, w.policy
	if _, err := w.store.Freeze(w.ctx, f); err != nil {
		t.Fatal(err)
	}
	c := w.compare(t, "from-postgres", w.gross, w.policy)
	if len(c.Causes) != 0 {
		t.Errorf("NUMERIC text was read as a change: %+v", c.Rows)
	}
}

// A report, once sent, does not change. Freezing the name again is refused
// whatever it carries, and the original is still what reads back.
func TestAFrozenReportCannotBeOverwritten(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)
	w.insert(t, "south", 2, 5, 0)

	ans, err := governance.Query(w.ctx, w.gross, byRegion, w.who, w.policy)
	if err != nil {
		t.Fatal(err)
	}
	f := FromAnswer(w.gross, byRegion, ans)
	f.Name, f.By = "0914-revenue", "someone else"
	if _, err := w.store.Freeze(w.ctx, f); !errors.Is(err, ErrExists) {
		t.Fatalf("second freeze of one name: err = %v, want ErrExists", err)
	}
	got, err := w.store.Get(w.ctx, "0914-revenue")
	if err != nil {
		t.Fatal(err)
	}
	if got.By != "王五" {
		t.Errorf("the sender was overwritten: %q", got.By)
	}
	for _, r := range got.Rows {
		if r[0] == "south" && toF(r[1]) != 50 {
			t.Errorf("the frozen south figure changed to %v", r[1])
		}
	}
}

// A correction is a new report that names what it corrects and why. The
// original stays, points at its correction, and cannot be corrected twice.
func TestACorrectionSupersedesWithoutRewritingTheOriginal(t *testing.T) {
	w := newWorld(t)
	w.deliver(t, "0914-revenue", byRegion, true)

	ans, err := governance.Query(w.ctx, w.gross, byRegion, w.who, w.policy)
	if err != nil {
		t.Fatal(err)
	}
	fix := FromAnswer(w.gross, byRegion, ans)
	fix.Name, fix.By, fix.Supersedes = "0914-revenue-r2", "王五", "0914-revenue"
	if _, err := w.store.Freeze(w.ctx, fix); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a correction with no reason: err = %v, want ErrInvalid", err)
	}
	fix.Note = "south was missing a late batch"
	if _, err := w.store.Freeze(w.ctx, fix); err != nil {
		t.Fatalf("correction: %v", err)
	}

	orig, err := w.store.Get(w.ctx, "0914-revenue")
	if err != nil {
		t.Fatal(err)
	}
	if orig.SupersededBy != "0914-revenue-r2" {
		t.Errorf("the original does not name its correction: %q", orig.SupersededBy)
	}
	if len(orig.Rows) != 2 {
		t.Errorf("the original's rows changed: %+v", orig.Rows)
	}

	again := fix
	again.Name = "0914-revenue-r3"
	if _, err := w.store.Freeze(w.ctx, again); !errors.Is(err, ErrSuperseded) {
		t.Errorf("a second correction of the same report: err = %v, want ErrSuperseded", err)
	}
	again.Supersedes = "no-such-report"
	if _, err := w.store.Freeze(w.ctx, again); !errors.Is(err, ErrNotFound) {
		t.Errorf("correcting a report that does not exist: err = %v, want ErrNotFound", err)
	}

	all, err := w.store.List(w.ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d reports, err %v", len(all), err)
	}
}

// A report whose definition cannot be named could never be attributed later,
// and a model that is not the one that produced the figures would be a false
// record of it. Both are refused at the door.
func TestAReportThatCouldNotBeAttributedLaterIsRefused(t *testing.T) {
	w := newWorld(t)
	f := Freezing{Name: "anon", By: "王五", Query: byRegion, Columns: []string{"region", "revenue"}}
	if _, err := w.store.Freeze(w.ctx, f); !errors.Is(err, ErrInvalid) {
		t.Errorf("no model hash: err = %v, want ErrInvalid", err)
	}
	f.ModelHash, f.Model = w.gross.ModelHash, []byte(modelNet)
	if _, err := w.store.Freeze(w.ctx, f); !errors.Is(err, ErrInvalid) {
		t.Errorf("model bytes that are not the hashed model: err = %v, want ErrInvalid", err)
	}
}
