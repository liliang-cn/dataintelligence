package intake

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// fixture is one customer database and one brain, both temp files.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	wh    *warehouse.Warehouse
	dsn   string
	brain string
	store *Store
}

// setup opens a writable SQLite warehouse — `mode=rwc` because OpenSQLite is
// read-only otherwise, which is right for a governed reader and wrong for a
// test that has to change the customer's schema under a signed plan — and a
// real cortexdb brain for the plans, the handle platform.Open passes.
func setup(t *testing.T) *fixture {
	t.Helper()
	// Nothing here calls a model; clearing these makes sure nothing can.
	for _, k := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL", "DI_AGENT_CLI", "DI_EMBED_BASE_URL"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	f := &fixture{t: t, ctx: context.Background(),
		dsn: filepath.Join(dir, "shop.db") + "?mode=rwc", brain: filepath.Join(dir, "brain.db")}
	wh, err := warehouse.OpenSQLite(f.ctx, f.dsn, warehouse.Options{})
	if err != nil {
		t.Fatalf("open warehouse: %v", err)
	}
	t.Cleanup(func() { _ = wh.Close() })
	f.wh = wh
	f.exec(`CREATE TABLE members (id INTEGER PRIMARY KEY, member_name TEXT, member_phone TEXT, store_id INTEGER)`)
	f.exec(`CREATE TABLE orders (id INTEGER PRIMARY KEY, member_id INTEGER, amount REAL, ship_address TEXT)`)
	f.exec(`CREATE TABLE _audit (ts TEXT, q TEXT)`)
	f.exec(`INSERT INTO members VALUES (1, '张三', '13800000000', 7)`)
	f.store = f.open()
	return f
}

func (f *fixture) open() *Store {
	f.t.Helper()
	cfg := cortexdb.DefaultConfig(f.brain)
	cfg.Dimensions = 4
	db, err := cortexdb.Open(cfg)
	if err != nil {
		f.t.Fatalf("open brain: %v", err)
	}
	f.t.Cleanup(func() { _ = db.Close() })
	n := 0
	s, err := New(db, WithClock(func() time.Time {
		n++
		return time.Date(2026, 9, 25, 9, 0, n, 0, time.UTC)
	}))
	if err != nil {
		f.t.Fatalf("new store: %v", err)
	}
	return s
}

func (f *fixture) exec(q string) {
	f.t.Helper()
	if _, err := f.wh.Exec(f.ctx, q); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) propose() Plan {
	f.t.Helper()
	p, err := f.store.Propose(f.ctx, f.wh, f.dsn, "engineer", "")
	if err != nil {
		f.t.Fatalf("propose: %v", err)
	}
	return p
}

func (f *fixture) signed() Plan {
	f.t.Helper()
	p := f.propose()
	p, err := f.store.Sign(f.ctx, p.ID, p.Short(), "王经理", "read it on the call")
	if err != nil {
		f.t.Fatalf("sign: %v", err)
	}
	return p
}

func TestAProposalMasksContactColumnsAndKeepsTheRest(t *testing.T) {
	f := setup(t)
	p := f.propose()
	if p.State != Draft || p.Hash == "" {
		t.Fatalf("proposed %+v", p)
	}
	want := map[string]Action{
		"members.id": Keep, "members.member_name": Keep, "members.member_phone": Mask,
		"members.store_id": Keep, "orders.id": Keep, "orders.member_id": Keep,
		"orders.amount": Keep, "orders.ship_address": Mask,
	}
	if len(p.Columns) != len(want) {
		t.Fatalf("plan covers %d columns, want %d (the platform's _audit is not the customer's): %+v",
			len(p.Columns), len(want), p.Columns)
	}
	for _, c := range p.Columns {
		if got := want[c.Table+"."+c.Column]; got != c.Action {
			t.Errorf("%s.%s = %s, want %s", c.Table, c.Column, c.Action, got)
		}
		if c.Personal != (c.Action == Mask) {
			t.Errorf("%s.%s: personal=%v but action %s", c.Table, c.Column, c.Personal, c.Action)
		}
	}
}

// The plan is kept, shown and backed up; the customer's password is none of
// those.
func TestAPlanNeverStoresTheCredential(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://meridian:s3cret@db:39632/meridian?sslmode=disable": "postgres://meridian@db:39632/meridian",
		"sqlserver://sa:s3cret@host:1433?database=x":                   "sqlserver://sa@host:1433",
		"host=db user=u password=s3cret dbname=m":                      "host=db user=u dbname=m",
		"host=db password='s3 cret' dbname=m":                          "host=db dbname=m",
		"root:s3cret@tcp(db:3306)/shop?parseTime=true":                 "root@tcp(db:3306)/shop",
		"/data/shop.db?mode=rwc":                                       "/data/shop.db",
	} {
		got := SourceOf(dsn)
		if strings.Contains(got, "s3") {
			t.Errorf("SourceOf(%q) = %q keeps the password", dsn, got)
		}
		if got != want {
			t.Errorf("SourceOf(%q) = %q, want %q", dsn, got, want)
		}
		if again := SourceOf(got); again != got {
			t.Errorf("SourceOf is not idempotent on %q: %q", got, again)
		}
	}
}

func TestEveryAmendmentChangesTheHashAndUndoingItRestoresIt(t *testing.T) {
	f := setup(t)
	p := f.propose()
	h0 := p.Hash

	p1, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "member_phone", Action: Redact}}, "王经理")
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if p1.Hash == h0 {
		t.Fatal("tightening a column left the hash unchanged — a signature over the old plan would carry over")
	}
	stored, _ := f.store.Get(f.ctx, p.ID)
	if stored.Hash != p1.Hash {
		t.Fatalf("returned %s, stored %s", p1.Short(), stored.Short())
	}
	if tr, _ := stored.Treatment("members", "member_phone"); tr.Action != Redact || tr.By != "王经理" {
		t.Errorf("amended treatment = %+v", tr)
	}

	// A whole table out of scope.
	p2, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "orders", Action: Redact}}, "王经理")
	if err != nil {
		t.Fatalf("amend table: %v", err)
	}
	if p2.Hash == p1.Hash {
		t.Fatal("redacting a table left the hash unchanged")
	}
	for _, c := range p2.Columns {
		if c.Table == "orders" && c.Action != Redact {
			t.Errorf("orders.%s = %s after redacting the table", c.Column, c.Action)
		}
	}

	// The hash is of the decisions, not of the history: undoing both is the
	// plan that was proposed.
	back, err := f.store.Amend(f.ctx, p.ID, []Change{
		{Table: "members", Column: "member_phone", Action: Mask},
		{Table: "orders", Action: Keep}, {Table: "orders", Column: "ship_address", Action: Mask},
	}, "王经理")
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if back.Hash != h0 {
		t.Errorf("undoing every amendment gives %s, the proposal was %s", back.Short(), short(h0))
	}
}

// A column moved within its table, or a reason reworded, is not a decision;
// the source is.
func TestTheHashIsOverDecisionsNotOrderOrWording(t *testing.T) {
	p := Plan{Source: "a", Driver: "sqlite", Columns: []Treatment{
		{Table: "t", Column: "x", Action: Keep}, {Table: "t", Column: "y", Action: Mask, Personal: true},
	}}
	h := planHash(p)
	p.Columns[0], p.Columns[1] = p.Columns[1], p.Columns[0]
	p.Columns[0].Reason, p.Columns[0].By = "reworded", "someone"
	if planHash(p) != h {
		t.Error("reordering or rewording changed the hash")
	}
	p.Source = "b"
	if planHash(p) == h {
		t.Error("a plan for another database hashes the same")
	}
}

func TestAmendRefusesAColumnThePlanDoesNotCover(t *testing.T) {
	f := setup(t)
	p := f.propose()
	_, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "id_card", Action: Redact}}, "王经理")
	if err == nil || !strings.Contains(err.Error(), "members.id_card") {
		t.Fatalf("amending a column that does not exist: %v", err)
	}
	if got, _ := f.store.Get(f.ctx, p.ID); got.Hash != p.Hash {
		t.Error("a refused amendment changed the plan")
	}
}

func TestAmendRefusesATreatmentThatIsNotOne(t *testing.T) {
	f := setup(t)
	p := f.propose()
	if _, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "member_phone", Action: "encrypt"}}, "王经理"); err == nil {
		t.Fatal("an unknown treatment was accepted")
	}
}

func TestAmendRefusesAnUnnamedAmender(t *testing.T) {
	f := setup(t)
	p := f.propose()
	if _, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "member_phone", Action: Keep}}, "  "); err == nil {
		t.Fatal("an amendment by nobody was accepted")
	}
}

func TestSignRefusesAnUnnamedSigner(t *testing.T) {
	f := setup(t)
	p := f.propose()
	_, err := f.store.Sign(f.ctx, p.ID, p.Hash, " ", "")
	if err == nil || !strings.Contains(err.Error(), "signer") {
		t.Fatalf("a signature by nobody: %v", err)
	}
	if got, _ := f.store.Get(f.ctx, p.ID); got.State != Draft {
		t.Errorf("state %s after a refused signature", got.State)
	}
}

// The signer read the plan, somebody amended it, the signer signed what they
// read. That signature is about a plan that no longer exists.
func TestSignRefusesAHashFromBeforeAnAmendment(t *testing.T) {
	f := setup(t)
	p := f.propose()
	read := p.Short()
	amended, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "member_phone", Action: Keep}}, "engineer")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Sign(f.ctx, p.ID, read, "王经理", "")
	if !errors.Is(err, ErrStaleHash) {
		t.Fatalf("signing the pre-amendment hash: %v", err)
	}
	if _, err := f.store.Sign(f.ctx, p.ID, amended.Short(), "王经理", ""); err != nil {
		t.Fatalf("signing the current hash: %v", err)
	}
}

// rollout.Sign accepts an empty hash; this does not. A signature that names
// nothing binds to nothing.
func TestSignRefusesAnEmptyOrTooShortHash(t *testing.T) {
	f := setup(t)
	p := f.propose()
	for _, h := range []string{"", p.Hash[:4]} {
		if _, err := f.store.Sign(f.ctx, p.ID, h, "王经理", ""); !errors.Is(err, ErrStaleHash) {
			t.Errorf("signing hash %q: %v", h, err)
		}
	}
}

// The row was edited in the brain directly, not through Amend. Whatever the
// signer read, nothing proves it was this.
func TestSignRefusesAPlanEditedBehindItsBack(t *testing.T) {
	f := setup(t)
	p := f.propose()
	tamper(t, f, p.ID)
	if _, err := f.store.Sign(f.ctx, p.ID, p.Hash, "王经理", ""); !errors.Is(err, ErrTampered) {
		t.Fatalf("signing a tampered plan: %v", err)
	}
}

func tamper(t *testing.T, f *fixture, id string) {
	t.Helper()
	q := f.store.dialect.Rebind(`UPDATE di_intake_plans SET treatments = replace(treatments, '"mask"', '"keep"') WHERE id = ?`)
	if _, err := f.store.db.ExecContext(f.ctx, q, id); err != nil {
		t.Fatal(err)
	}
}

func TestASignedPlanIsSupersededNotEdited(t *testing.T) {
	f := setup(t)
	p := f.signed()
	if _, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "members", Column: "member_phone", Action: Keep}}, "engineer"); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("amending a signed plan: %v", err)
	}
	if _, err := f.store.Sign(f.ctx, p.ID, p.Hash, "someone else", ""); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("re-signing a signed plan: %v", err)
	}
}

func TestSigningANewPlanSupersedesTheOldAndTheLedgerSaysSo(t *testing.T) {
	f := setup(t)
	first := f.signed()
	next := f.propose()
	next, err := f.store.Amend(f.ctx, next.ID, []Change{{Table: "orders", Column: "amount", Action: Mask}}, "财务")
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.store.Sign(f.ctx, next.ID, next.Hash, "王经理", "finance asked")
	if err != nil {
		t.Fatalf("sign the second: %v", err)
	}
	if next.Supersedes != first.ID {
		t.Errorf("supersedes %q, want %q", next.Supersedes, first.ID)
	}
	if old, _ := f.store.Get(f.ctx, first.ID); old.State != Superseded {
		t.Errorf("first plan is %s", old.State)
	}
	cur, err := f.store.Current(f.ctx, f.dsn)
	if err != nil || cur.ID != next.ID {
		t.Fatalf("current = %s, %v", cur.ID, err)
	}

	led, err := f.store.Ledger(f.ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	var acts []string
	for _, e := range led {
		acts = append(acts, e.Act+":"+e.By)
	}
	want := "propose:engineer sign:王经理 propose:engineer amend:财务 supersede:王经理 sign:王经理"
	if got := strings.Join(acts, " "); got != want {
		t.Fatalf("ledger = %s\nwant     %s", got, want)
	}
	amend := led[3]
	if amend.FromHash == "" || amend.FromHash == amend.ToHash || amend.ToHash != next.Hash {
		t.Errorf("the amendment's line does not carry the hash it moved from and to: %+v", amend)
	}
	if !strings.Contains(amend.Note, "orders.amount→mask") {
		t.Errorf("the amendment's line does not say what changed: %q", amend.Note)
	}
	if led[5].ToHash != next.Hash {
		t.Errorf("the signature's line names %s, the plan is %s", led[5].ToHash, next.Hash)
	}
}

func TestRequireRefusesADatabaseNobodySignedFor(t *testing.T) {
	f := setup(t)
	if _, err := f.store.Require(f.ctx, f.wh, f.dsn); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("no plan at all: %v", err)
	}
	p := f.propose()
	_, err := f.store.Require(f.ctx, f.wh, f.dsn)
	if !errors.Is(err, ErrUnsigned) {
		t.Fatalf("a draft opened the gate: %v", err)
	}
	if !strings.Contains(err.Error(), p.ID) || !strings.Contains(err.Error(), p.Short()) {
		t.Errorf("the refusal does not name the draft waiting to be signed: %v", err)
	}
}

func TestRequirePassesTheSignedPlan(t *testing.T) {
	f := setup(t)
	s := f.signed()
	p, err := f.store.Require(f.ctx, f.wh, f.dsn)
	if err != nil {
		t.Fatalf("require: %v", err)
	}
	if p.ID != s.ID || p.SignedBy != "王经理" {
		t.Errorf("gate returned %+v", p)
	}
	if tr, ok := p.Treatment("members", "member_phone"); !ok || tr.Action != Mask {
		t.Errorf("the plan in force says %+v for the phone column", tr)
	}
}

// The case that matters most: the customer's DBA adds an id_card column after
// the plan was signed. Nobody decided whether DI may read it.
func TestRequireRefusesAColumnAddedAfterSigning(t *testing.T) {
	f := setup(t)
	f.signed()
	f.exec(`ALTER TABLE members ADD COLUMN id_card TEXT`)
	_, err := f.store.Require(f.ctx, f.wh, f.dsn)
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("a new column passed the gate: %v", err)
	}
	if !strings.Contains(err.Error(), "members.id_card") {
		t.Errorf("the refusal does not name the column: %v", err)
	}
}

func TestRequireRefusesATableAddedAfterSigning(t *testing.T) {
	f := setup(t)
	f.signed()
	f.exec(`CREATE TABLE payroll (staff_id INTEGER, salary REAL)`)
	_, err := f.store.Require(f.ctx, f.wh, f.dsn)
	if !errors.Is(err, ErrDrift) || !strings.Contains(err.Error(), "payroll.salary") {
		t.Fatalf("a new table passed the gate: %v", err)
	}
}

// A column that is gone cannot be read; refusing on it would demand a
// signature for nothing. It is reported, and so is a changed type.
func TestADroppedOrRetypedColumnIsReportedButDoesNotClose(t *testing.T) {
	f := setup(t)
	f.signed()
	f.exec(`ALTER TABLE orders DROP COLUMN ship_address`)
	if _, err := f.store.Require(f.ctx, f.wh, f.dsn); err != nil {
		t.Fatalf("a dropped column closed the gate: %v", err)
	}
	r, err := f.store.Check(f.ctx, f.wh, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gone) != 1 || r.Gone[0] != "orders.ship_address" || len(r.Unclassified) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestRequireRefusesAPlanTamperedAfterSigning(t *testing.T) {
	f := setup(t)
	p := f.signed()
	tamper(t, f, p.ID)
	if _, err := f.store.Require(f.ctx, f.wh, f.dsn); !errors.Is(err, ErrTampered) {
		t.Fatalf("a signed plan edited in the brain still opened the gate: %v", err)
	}
}

func TestAPlanSignedForOneDatabaseDoesNotOpenAnother(t *testing.T) {
	f := setup(t)
	f.signed()
	other := filepath.Join(t.TempDir(), "other.db")
	if _, err := f.store.Require(f.ctx, f.wh, other); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("a signature for %s opened %s: %v", SourceOf(f.dsn), other, err)
	}
	// The same database spelled with a different mode is the same database.
	ro := strings.TrimSuffix(f.dsn, "?mode=rwc") + "?mode=ro"
	if _, err := f.store.Require(f.ctx, f.wh, ro); err != nil {
		t.Fatalf("the same file opened read-only is refused: %v", err)
	}
}

// The signature is a record, not a process-lifetime flag.
func TestTheSignatureSurvivesReopeningTheBrain(t *testing.T) {
	f := setup(t)
	s := f.signed()
	again := f.open()
	p, err := again.Require(f.ctx, f.wh, f.dsn)
	if err != nil {
		t.Fatalf("after reopening: %v", err)
	}
	if p.ID != s.ID || p.Hash != s.Hash || !p.SignedAt.Equal(s.SignedAt) {
		t.Errorf("read back %+v, signed %+v", p, s)
	}
}

// The index is the second lock on "one plan in force": a second path to
// signing that forgot to supersede is refused by the table.
func TestTheTableRefusesTwoSignedPlansForOneSource(t *testing.T) {
	f := setup(t)
	f.signed()
	p := f.propose()
	q := f.store.dialect.Rebind(`UPDATE di_intake_plans SET state = 'signed' WHERE id = ?`)
	if _, err := f.store.db.ExecContext(f.ctx, q, p.ID); err == nil {
		t.Fatal("two plans are in force for one database")
	}
}

func Example_source() {
	fmt.Println(SourceOf("postgres://di:pw@warehouse:5432/shop?sslmode=require"))
	// Output: postgres://di@warehouse:5432/shop
}

// Amending a tampered draft would re-hash the tampered treatments and launder
// them into a plan that verifies.
func TestAmendRefusesAPlanEditedBehindItsBack(t *testing.T) {
	f := setup(t)
	p := f.propose()
	tamper(t, f, p.ID)
	_, err := f.store.Amend(f.ctx, p.ID, []Change{{Table: "orders", Column: "amount", Action: Mask}}, "engineer")
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("amending a tampered plan: %v", err)
	}
}
