package rollout

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/dataintelligence/engine"
)

// The join is a hash computed in two packages. If they ever disagree, every
// answer looks unattested and nothing says why — so the agreement is a test,
// not a comment.
func TestTheEngineAndTheRegistryHashAModelTheSameWay(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "m.yaml", modelA)

	eng, err := engine.New(context.Background(), path,
		"sqlite://"+filepath.Join(dir, "w.db")+"?mode=rwc")
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	defer eng.Close()

	want, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if eng.ModelHash != want {
		t.Fatalf("engine stamps %q, registry stores %q — the trail could never be joined to the ledger",
			eng.ModelHash, want)
	}
	if eng.ModelHash == "" {
		t.Fatal("a modelled engine carries no hash")
	}
}

// An engine with no model must not claim to be running one.
func TestAnUnmodelledDatabaseCarriesNoHash(t *testing.T) {
	dir := t.TempDir()
	eng, err := engine.New(context.Background(), "",
		"sqlite://"+filepath.Join(dir, "w.db")+"?mode=rwc")
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	defer eng.Close()
	if eng.ModelHash != "" {
		t.Errorf("an unmodelled database stamped %q on its answers", eng.ModelHash)
	}
}

// The end of the chain: given a trail, say which answers came from a definition
// somebody approved and which came from one nobody did.
func TestAttestSeparatesApprovedAnswersFromUnaccountedOnes(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()

	// One model goes through the registry properly.
	signedPath := write(t, dir, "signed.yaml", modelA)
	if _, err := r.Register(ctx, "v1", signedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v1", "", "张三", "口径复核"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	signedHash, _ := HashFile(signedPath)

	// Another answers questions without ever being registered — someone
	// pointed the server at a YAML file by hand.
	roguePath := write(t, dir, "rogue.yaml", modelB)
	rogueHash, _ := HashFile(roguePath)

	// Two answers from the approved model, three from the unregistered one.
	seedTrail(t, r, signedHash, 2)
	seedTrail(t, r, rogueHash, 3)

	all, err := r.Attest(ctx)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("attest reports %d models: %+v", len(all), all)
	}
	// Busiest first, so the biggest exposure is the first line read.
	if all[0].Hash != rogueHash || all[0].Answers != 3 {
		t.Fatalf("first row is %+v, want the rogue model with 3 answers", all[0])
	}
	if all[0].Promoted || all[0].SignedBy != "" {
		t.Errorf("an unregistered model was reported as approved: %+v", all[0])
	}
	if !all[1].Promoted || all[1].SignedBy != "张三" {
		t.Errorf("the approved model lost its signature: %+v", all[1])
	}
	if all[1].Note != "口径复核" {
		t.Errorf("the signer's reason did not survive the join: %q", all[1].Note)
	}

	bad := Unattested(all)
	if len(bad) != 1 || bad[0].Hash != rogueHash {
		t.Fatalf("Unattested = %+v, want only the rogue model", bad)
	}
}

// seedTrail writes n audit rows for one model hash, standing in for n answers.
func seedTrail(t *testing.T, r *Registry, hash string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _audit (
		ts TEXT, "user" TEXT, role TEXT, metrics TEXT, group_by TEXT,
		"sql" TEXT, refused INTEGER, note TEXT, question TEXT,
		engagement TEXT, model_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := r.wh.Exec(ctx,
			`INSERT INTO _audit (ts, "user", metrics, model_hash) VALUES (?,?,?,?)`,
			"2026-09-22T02:00:00Z", "someone", "[revenue]", hash); err != nil {
			t.Fatal(err)
		}
	}
}

// seedTrailAt writes n audit rows for one hash at one moment, in whatever
// shape the engine would have written the timestamp.
func seedTrailAt(t *testing.T, r *Registry, hash, ts string, n int) {
	t.Helper()
	seedTrail(t, r, hash, 0)
	for i := 0; i < n; i++ {
		if _, err := r.wh.Exec(context.Background(),
			`INSERT INTO _audit (ts, "user", metrics, model_hash) VALUES (?,?,?,?)`,
			ts, "someone", "[revenue]", hash); err != nil {
			t.Fatal(err)
		}
	}
}

// A signature given today does not approve figures sent last month.
//
// The join is on the hash, so without the time every answer those bytes ever
// produced read as approved the moment somebody signed them. On a real trail
// the report said "125 of 125 answers came from an approved definition" about
// answers that were all given before the signature existed.
func TestAnswersGivenBeforeTheSignatureAreNotCountedAsApprovedWhenGiven(t *testing.T) {
	r, dir := registry(t) // the registry's clock reads 2026-09-22T00:00:0NZ
	ctx := context.Background()
	path := write(t, dir, "m.yaml", modelA)
	hash, _ := HashFile(path)

	// Three answers the day before anybody signed, in SQLite's own
	// datetime('now') shape; two after, in RFC 3339.
	seedTrailAt(t, r, hash, "2026-09-21 10:00:00", 3)

	if _, err := r.Register(ctx, "v1", path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v1", "", "张三", "口径复核"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	seedTrailAt(t, r, hash, "2026-09-22T05:00:00Z", 2)

	all, err := r.Attest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %+v", all)
	}
	a := all[0]
	if a.Answers != 5 || !a.Promoted {
		t.Fatalf("got %+v", a)
	}
	if a.BeforeApproval != 3 {
		t.Errorf("BeforeApproval = %d, want 3: answers given before the signature were counted as approved", a.BeforeApproval)
	}
	if a.ApprovedAt == "" {
		t.Error("the report does not say when the definition went live")
	}
}

// An answer whose time cannot be read is not claimed as approved.
func TestAnAnswerWithAnUnreadableTimeIsNotClaimedAsApproved(t *testing.T) {
	if got := countBefore([]answerTime{{ok: false}}, "2026-09-22T00:00:01Z"); got != 1 {
		t.Errorf("an unreadable timestamp was counted as after approval")
	}
	for _, v := range []any{"2026-09-21 10:00:00", "2026-09-21T10:00:00Z", []byte("2026-09-21 10:00:00"), "2026-09-21 10:00:00.123456+00"} {
		if _, ok := parseWhen(v); !ok {
			t.Errorf("parseWhen(%#v) could not read an engine's timestamp", v)
		}
	}
}
