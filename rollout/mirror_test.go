package rollout

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// flakyEmbedder is a deterministic stand-in for an embedding endpoint that can
// be told to go down. RecordDecision embeds the note, so a failing embedder is
// a brain that refuses every decision — the failure a mirror has to survive —
// without reaching into the store to break it.
type flakyEmbedder struct{ down *atomic.Bool }

func (flakyEmbedder) Dim() int { return 16 }

func (e flakyEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if e.down != nil && e.down.Load() {
		return nil, errors.New("embedding endpoint unreachable")
	}
	sum := sha256.Sum256([]byte(text))
	v := make([]float32, 16)
	for i := range v {
		v[i] = float32(int(sum[i])-128) / 128
	}
	return v, nil
}

func (e flakyEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func openBrain(t *testing.T, down *atomic.Bool) *cortexdb.DB {
	t.Helper()
	cfg := cortexdb.DefaultConfig(filepath.Join(t.TempDir(), "brain.db"))
	cfg.Dimensions = 16
	db, err := cortexdb.Open(cfg, cortexdb.WithEmbedder(flakyEmbedder{down: down}))
	if err != nil {
		t.Fatalf("open brain: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ship registers, signs and promotes one version, failing the test on any
// error the act itself returns.
func ship(t *testing.T, r *Registry, dir, name, file, body, by, note string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Register(ctx, name, write(t, dir, file, body)); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if _, err := r.Sign(ctx, name, "", by, note); err != nil {
		t.Fatalf("sign %s: %v", name, err)
	}
	if _, err := r.Promote(ctx, name); err != nil {
		t.Fatalf("promote %s: %v", name, err)
	}
}

// entryFor is the newest ledger line for one act on one version.
func entryFor(t *testing.T, r *Registry, act, name string) Entry {
	t.Helper()
	entries, err := r.Ledger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := latest(entries, -1, func(e Entry) bool { return e.Act == act && e.Name == name })
	if i < 0 {
		t.Fatalf("no %s line for %s in the ledger", act, name)
	}
	return entries[i]
}

func decisionFor(t *testing.T, db *cortexdb.DB, e Entry) cortexdb.DecisionRecord {
	t.Helper()
	chain, err := db.DecisionChain(context.Background(), DecisionIDFor(e), 1)
	if err != nil {
		t.Fatalf("the %s of %s is not in the brain: %v", e.Act, e.Name, err)
	}
	return chain.Decisions[0]
}

func strictReport(t *testing.T) func(error) {
	return func(err error) { t.Errorf("mirror failed: %v", err) }
}

// A promotion's decision is credited to the person who signed the model, and
// rests on their signature — the same person, for the same reason, as the
// ledger line beside it.
func TestAPromotionIsCreditedToTheSignerAndRestsOnTheirSignature(t *testing.T) {
	r, dir := registry(t)
	db := openBrain(t, nil)
	r.WithBrain(db).OnMirrorError(strictReport(t))

	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")
	ship(t, r, dir, "v2", "b.yaml", modelB, "zhang", "units now counts returns")

	sign := decisionFor(t, db, entryFor(t, r, "sign", "v2"))
	promote := decisionFor(t, db, entryFor(t, r, "promote", "v2"))
	hash, _ := HashFile(filepath.Join(dir, "b.yaml"))

	if sign.Actor != "zhang" || sign.Kind != "rollout.sign" {
		t.Errorf("sign decision is %+v, want zhang's rollout.sign", sign)
	}
	if promote.Actor != "zhang" {
		t.Errorf("promotion credited to %q, want the signer zhang", promote.Actor)
	}
	if promote.Grade != cortexdb.GradeVerified {
		t.Errorf("a signed promotion is graded %q, want verified", promote.Grade)
	}
	if promote.Subject != ModelVersionNodeID(hash) {
		t.Errorf("promotion is about %q, want the model's bytes %q", promote.Subject, ModelVersionNodeID(hash))
	}
	if len(promote.Premises) != 1 || promote.Premises[0].ID != sign.ID {
		t.Fatalf("promotion rests on %+v, want only the signature %s", promote.Premises, sign.ID)
	}
	if !strings.Contains(promote.Note, "units now counts returns") {
		t.Errorf("the signer's reason did not reach the decision: %q", promote.Note)
	}
}

// Promoting v2 over v1 supersedes the decision that made v1 live, so the chain
// from the newest promotion walks back through every release before it.
func TestPromotingOverALiveVersionSupersedesTheDecisionThatMadeItLive(t *testing.T) {
	r, dir := registry(t)
	db := openBrain(t, nil)
	r.WithBrain(db).OnMirrorError(strictReport(t))

	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")
	ship(t, r, dir, "v2", "b.yaml", modelB, "zhang", "units now counts returns")

	first := DecisionIDFor(entryFor(t, r, "promote", "v1"))
	second := decisionFor(t, db, entryFor(t, r, "promote", "v2"))
	if len(second.Supersedes) != 1 || second.Supersedes[0] != first {
		t.Fatalf("v2's promotion supersedes %v, want v1's %s", second.Supersedes, first)
	}
	if len(decisionFor(t, db, entryFor(t, r, "promote", "v1")).Supersedes) != 0 {
		t.Error("the first promotion claims to supersede something")
	}

	h, err := r.History(context.Background(), "v2")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if h.Head != second.ID {
		t.Errorf("history starts at %s, want the newest act %s", h.Head, second.ID)
	}
	who := map[string]string{}
	for _, d := range h.Chain.Decisions {
		who[d.ID] = d.Actor
	}
	for act, want := range map[string]string{
		DecisionIDFor(entryFor(t, r, "sign", "v2")): "zhang",
		first: "li",
		DecisionIDFor(entryFor(t, r, "sign", "v1")): "li",
	} {
		if who[act] != want {
			t.Errorf("chain credits %s to %q, want %q (chain: %v)", act, who[act], want, who)
		}
	}
	if len(h.Unmirrored) != 0 {
		t.Errorf("a healthy brain reports unmirrored lines: %+v", h.Unmirrored)
	}
}

// Going back is a decision too: the rollback rests on the restored version's
// original signature and supersedes the promotion it undid.
func TestARollbackRestsOnTheOriginalSignatureAndSupersedesWhatItUndid(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	db := openBrain(t, nil)
	r.WithBrain(db).OnMirrorError(strictReport(t))

	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")
	ship(t, r, dir, "v2", "b.yaml", modelB, "zhang", "units now counts returns")

	// v2 is pulled from service, leaving nothing active — the case in which
	// Rollback restores the newest retired version.
	v2, err := r.Get(ctx, "v2")
	if err != nil {
		t.Fatal(err)
	}
	v2.Status = StatusCandidate
	if err := r.save(ctx, v2); err != nil {
		t.Fatal(err)
	}
	restored, err := r.Rollback(ctx)
	if err != nil || restored == nil || restored.Name != "v1" {
		t.Fatalf("rollback restored %+v, %v; want v1", restored, err)
	}

	back := decisionFor(t, db, entryFor(t, r, "rollback", "v1"))
	if back.Actor != "li" {
		t.Errorf("rollback credited to %q, want v1's signer li", back.Actor)
	}
	if len(back.Premises) != 1 || back.Premises[0].ID != DecisionIDFor(entryFor(t, r, "sign", "v1")) {
		t.Errorf("rollback rests on %+v, want v1's signature", back.Premises)
	}
	undone := DecisionIDFor(entryFor(t, r, "promote", "v2"))
	if len(back.Supersedes) != 1 || back.Supersedes[0] != undone {
		t.Errorf("rollback supersedes %v, want v2's promotion %s", back.Supersedes, undone)
	}

	h, err := r.History(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if h.Head != back.ID {
		t.Errorf("v1's history starts at %s, want the rollback %s", h.Head, back.ID)
	}
	if len(h.About) != 3 { // signed, promoted, restored
		t.Errorf("decisions about v1's bytes: %d, want 3: %+v", len(h.About), h.About)
	}
}

// A version promoted a second time is a second decision. Keying the id on the
// version alone would overwrite the first go-live and loop the chain.
func TestPromotingARetiredVersionAgainIsANewDecisionNotAnOverwrite(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	db := openBrain(t, nil)
	r.WithBrain(db).OnMirrorError(strictReport(t))

	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")
	firstLine := entryFor(t, r, "promote", "v1")
	ship(t, r, dir, "v2", "b.yaml", modelB, "zhang", "units now counts returns")
	if _, err := r.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	again := entryFor(t, r, "promote", "v1")
	if DecisionIDFor(again) == DecisionIDFor(firstLine) {
		t.Fatal("the second promotion of v1 has the first one's id")
	}
	first := decisionFor(t, db, firstLine)
	if len(first.Supersedes) != 0 {
		t.Errorf("the original go-live was rewritten to supersede %v", first.Supersedes)
	}
	if got := decisionFor(t, db, again).Supersedes; len(got) != 1 || got[0] != DecisionIDFor(entryFor(t, r, "promote", "v2")) {
		t.Errorf("the re-promotion supersedes %v, want v2's promotion", got)
	}
}

// The brain going down costs the mirror, never the act: the promotion commits,
// the ledger has it, the call succeeds, the failure is reported, and Resync
// puts the chain back once the brain is up.
func TestABrainFailureNeitherFailsNorUndoesThePromotion(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	var down atomic.Bool
	down.Store(true)
	db := openBrain(t, &down)
	var reported []*MirrorError
	r.WithBrain(db).OnMirrorError(func(err error) {
		var me *MirrorError
		if !errors.As(err, &me) {
			t.Errorf("reported %T, want *MirrorError: %v", err, err)
			return
		}
		reported = append(reported, me)
	})

	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut") // fails the test if any act errors

	active, err := r.Active(ctx)
	if err != nil || active.Name != "v1" {
		t.Fatalf("after a brain failure the active version is %+v, %v; want v1", active, err)
	}
	if e := entryFor(t, r, "promote", "v1"); e.By != "li" {
		t.Errorf("the ledger line is %+v", e)
	}
	if len(reported) != 2 || reported[0].Act != "sign" || reported[1].Act != "promote" {
		t.Fatalf("reported %+v, want the sign and the promote", reported)
	}

	h, err := r.History(ctx, "v1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if h.Head != "" || len(h.Unmirrored) != 2 {
		t.Errorf("history hides the gap: head %q, unmirrored %+v", h.Head, h.Unmirrored)
	}

	down.Store(false)
	n, err := r.Resync(ctx)
	if err != nil || n != 2 {
		t.Fatalf("resync wrote %d, %v; want 2", n, err)
	}
	if n, _ := r.Resync(ctx); n != 0 {
		t.Errorf("a second resync wrote %d decisions, want 0", n)
	}
	h, err = r.History(ctx, "v1")
	if err != nil || len(h.Unmirrored) != 0 || len(h.Chain.Decisions) != 2 {
		t.Fatalf("after resync: %+v, %v", h, err)
	}
}

// A brain attached after the fact fills in, from the ledger, whatever the new
// decision needs to point at.
func TestABrainAttachedLateBackfillsWhatTheNewDecisionRestsOn(t *testing.T) {
	r, dir := registry(t)
	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")

	db := openBrain(t, nil)
	r.WithBrain(db).OnMirrorError(strictReport(t))
	ship(t, r, dir, "v2", "b.yaml", modelB, "zhang", "units now counts returns")

	second := decisionFor(t, db, entryFor(t, r, "promote", "v2"))
	first := DecisionIDFor(entryFor(t, r, "promote", "v1"))
	if len(second.Supersedes) != 1 || second.Supersedes[0] != first {
		t.Fatalf("v2's promotion supersedes %v, want the backfilled %s", second.Supersedes, first)
	}
	if d := decisionFor(t, db, entryFor(t, r, "promote", "v1")); d.Actor != "li" || len(d.Premises) != 1 {
		t.Errorf("backfilled promotion is %+v, want li's, resting on the signature", d)
	}
}

// Without a brain the registry works as it always did, and the brain-only
// reads say the brain is missing instead of reporting an empty history.
func TestWithoutABrainHistorySaysSoInsteadOfReturningNothing(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	ship(t, r, dir, "v1", "a.yaml", modelA, "li", "first cut")

	if _, err := r.History(ctx, "v1"); !errors.Is(err, ErrNoBrain) {
		t.Errorf("History without a brain = %v, want ErrNoBrain", err)
	}
	if _, err := r.Resync(ctx); !errors.Is(err, ErrNoBrain) {
		t.Errorf("Resync without a brain = %v, want ErrNoBrain", err)
	}
	if entries, err := r.Ledger(ctx); err != nil || len(entries) != 2 {
		t.Errorf("the ledger without a brain has %d lines, %v; want 2", len(entries), err)
	}
}
