package rollout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// registry opens a writable SQLite warehouse. `mode=rwc` is load-bearing:
// warehouse.OpenSQLite opens read-only unless the DSN already says otherwise,
// which is right for a governed reader and wrong for the table the registry
// keeps its own records in.
func registry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	wh, err := warehouse.OpenSQLite(context.Background(),
		filepath.Join(dir, "registry.db")+"?mode=rwc", warehouse.Options{})
	if err != nil {
		t.Fatalf("open warehouse: %v", err)
	}
	t.Cleanup(func() { _ = wh.Close() })
	n := 0
	return New(wh, func() string { n++; return fmtTime(n) }), dir
}

func fmtTime(n int) string {
	return "2026-09-22T00:00:" + string(rune('0'+n/10)) + string(rune('0'+n%10)) + "Z"
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The registry persists on every engine it claims to support, not only on the
// one whose SQL spellings it was written in.
func TestTheRegistryRunsOnSQLiteAtAll(t *testing.T) {
	r, dir := registry(t)
	v, err := r.Register(context.Background(), "v1", write(t, dir, "a.yaml", modelA))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if v.Hash == "" || v.Status != StatusCandidate {
		t.Fatalf("registered %+v", v)
	}
	// Registering the same name again must overwrite rather than collide: the
	// upsert is two statements now, and this is what says they add up to one.
	if _, err := r.Register(context.Background(), "v1", write(t, dir, "a.yaml", modelA)); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	all, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("re-registering made %d rows, want 1", len(all))
	}
}

// Nothing reaches production unapproved.
func TestPromoteRefusesAnUnsignedModel(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	if _, err := r.Register(ctx, "v1", write(t, dir, "a.yaml", modelA)); err != nil {
		t.Fatal(err)
	}
	_, err := r.Promote(ctx, "v1")
	if err == nil {
		t.Fatal("an unsigned model was promoted")
	}
	if !strings.Contains(err.Error(), "not been signed") {
		t.Errorf("refusal does not say why: %v", err)
	}
}

// The gap this whole file exists for: Register hashes the file, Promote read it
// again, and nothing checked that the two reads saw the same bytes.
func TestPromoteRefusesAModelEditedAfterItWasSigned(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	path := write(t, dir, "a.yaml", modelA)
	if _, err := r.Register(ctx, "v1", path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v1", "", "li", "reviewed the revenue definition"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Someone edits the YAML between the approval and the promotion.
	write(t, dir, "a.yaml", modelB)

	if _, err := r.Promote(ctx, "v1"); err == nil {
		t.Fatal("promoted a model that was edited after it was signed")
	} else if !strings.Contains(err.Error(), "changed after it was approved") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
}

// A signature names a hash, not a version name.
func TestSigningAHashYouDidNotReadIsRefused(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()
	if _, err := r.Register(ctx, "v1", write(t, dir, "a.yaml", modelA)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v1", "deadbeef0000", "li", ""); err == nil {
		t.Fatal("signed a hash that is not the model's")
	}
	if _, err := r.Sign(ctx, "v1", "", "", ""); err == nil {
		t.Fatal("accepted a signature with no signer")
	}
}

// The end the product is for: after a promotion, the ledger answers who changed
// the definition, when, and which metrics moved.
func TestTheLedgerSaysWhoChangedTheNumbersAndWhichOnesMoved(t *testing.T) {
	r, dir := registry(t)
	ctx := context.Background()

	a := write(t, dir, "a.yaml", modelA)
	if _, err := r.Register(ctx, "v1", a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v1", "", "li", "first cut"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Promote(ctx, "v1"); err != nil {
		t.Fatal(err)
	}

	b := write(t, dir, "b.yaml", modelB)
	if _, err := r.Register(ctx, "v2", b); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sign(ctx, "v2", "", "zhang", "units now counts returns"); err != nil {
		t.Fatal(err)
	}
	changed, err := r.Promote(ctx, "v2")
	if err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("lineage delta = %v, want units and tax", changed)
	}

	entries, err := r.Ledger(ctx)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	// sign, promote, sign, promote.
	if len(entries) != 4 {
		t.Fatalf("ledger has %d entries: %+v", len(entries), entries)
	}
	last := entries[3]
	if last.Act != "promote" || last.Name != "v2" {
		t.Fatalf("last entry is %+v", last)
	}
	// The signer, not whoever ran the command.
	if last.By != "zhang" {
		t.Errorf("ledger credits %q, want the signer zhang", last.By)
	}
	if !strings.Contains(last.Changed, "units") || !strings.Contains(last.Changed, "tax") {
		t.Errorf("ledger does not say which metrics moved: %q", last.Changed)
	}
	if last.Note != "units now counts returns" {
		t.Errorf("the signer's reason is missing: %q", last.Note)
	}
	// And the version it replaced is named, so the trail is a chain.
	if last.FromHash == "" || last.FromHash == last.ToHash {
		t.Errorf("promotion does not record what it replaced: from=%q to=%q", last.FromHash, last.ToHash)
	}
}
