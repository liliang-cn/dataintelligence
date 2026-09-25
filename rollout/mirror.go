package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

// The registry's acts, as decisions in the brain — beside the ledger, not
// instead of it.
//
// CortexDB has had first-class decisions since v2.98: a Decision node,
// `based_on` / `about` / `supersedes` edges, graded verified because a named
// actor signed it, and DecisionChain / Precedents to read it back. `_model_ledger`
// is a second implementation of the same idea, written before that existed.
// Everything else that decides something on this platform — an agent through
// decision_record, athanor's loads and reviews and rule firings — lands in the
// brain. Who approved a definition of revenue did not, so "why is this number
// defined this way, and what did it replace" could not be asked in the same
// place as every other why.
//
// # Why the ledger stays the source of truth
//
// `_model_ledger` lives in the warehouse, next to `_audit`, and Attest is a
// join of the two on the model hash. The brain is a different database — a
// different file, often a different engine — and a join across the two is not
// a query anybody can write. Moving the ledger into the brain would keep the
// "who decided" half and lose the half that makes it worth having: "and these
// four thousand answers came from what they decided". So the ledger is written
// first, exactly as before, and the brain gets a mirror of each line after it.
// Every decision here is derivable from the ledger, and nothing reads the brain
// to decide anything the ledger does not already say.
//
// # Ids: one ledger line, one decision
//
// A decision's id is `di:rollout:<act>:<name>:<hash>:<ledger ts>` — the
// identity of the ledger line it mirrors, spelled out. Three things follow:
//
//   - It is stable. RecordDecision upserts on a given id, so mirroring the same
//     line twice converges on one entry, which is what makes Resync safe to run
//     as often as anyone likes.
//   - It is per act, not per version. A shorter `…:promote:<name>:<hash>` reads
//     better and is wrong the day a retired version is promoted again: the
//     second promotion would overwrite the first, its `supersedes` edge would
//     point at the promotion that had superseded it, and the chain would read
//     as a loop with the original go-live date gone. The timestamp is what
//     tells two promotions of the same bytes apart.
//   - It can be computed from the ledger alone. A promote finds its sign
//     decision and the decision it supersedes by reading earlier ledger lines,
//     never by asking the brain what it happens to hold — so a brain attached
//     late, or one that missed a write, is filled in from the same record that
//     is authoritative anyway.
//
// # Who the actor is
//
// The signer, for all three acts — the same person `_model_ledger` credits.
// Promote's comment says why the ledger does it: the judgement was made when
// the model was signed, and crediting the operator who typed the command would
// put a name on a decision they did not make. A rollback restores a model on
// the strength of its original signature, so it is credited to that signature
// too. The mirror copies the ledger's `by` column rather than deciding again,
// because two records disagreeing about who decided would be worse than either
// alone.
//
// # What each decision points at
//
//   - about: a `di_model_version:<hash>` node. A hash rather than a version
//     name, for the reason Attest joins on one — the name says which registry
//     row, the hash says which bytes — and the node is created here because,
//     unlike athanor's job ids, a model version is a real thing that other
//     decisions will be about.
//   - sign: nothing else. It is the judgement; it rests on the person.
//   - promote: based_on the sign decision for the same name and bytes, and, when
//     it replaced an active version, supersedes the decision that made that
//     version active.
//   - rollback: based_on the sign decision of the version it restores — the
//     approval Rollback deliberately does not ask for again — and supersedes
//     whatever was last made active, which is the thing going back undoes.
//
// So a chain from the newest promotion reads: promoted v2 → signed v2, and
// superseded promoted v1 → signed v1. A rollback slots in the same way.
//
// # When the brain write fails
//
// The act stands and the call succeeds. This is the opposite of what record()
// does, and deliberately: record() returns its error because a promotion
// nobody wrote down is the case this package exists to prevent. By the time
// the mirror runs, it has been written down — in the ledger that Attest reads.
// A Promote that returned an error here would tell a caller the promotion had
// failed when it had not, and `di rollout promote` would skip invalidating the
// caches whose definitions just changed, turning a missing graph node into
// stale numbers. The failure is reported instead, the way athanor reports a
// LedgerError beside an act that stood: to OnMirrorError's function, or the
// log if nobody registered one; and History lists every ledger line the brain
// does not hold, so a gap is visible to the next person who asks. Resync
// closes it.

// ModelVersionNodeType is the node every rollout decision is about.
const ModelVersionNodeType = "di_model_version"

// decisionSource is the knowledge contract's `_source` on these entries.
// CortexDB's default, "decision-ledger", would be true; this tells an auditor
// the registry wrote the entry rather than an agent calling decision_record.
const decisionSource = "di-rollout"

// decisionKindPrefix namespaces the kinds. Signing and promoting are this
// product's workflow, not a shape a general ledger has an opinion about, and a
// prefix keeps Precedents by kind from mixing them with athanor's reviews on a
// shared brain.
const decisionKindPrefix = "rollout."

// chainDepth is how far History walks. A promotion chain is one hop per
// release, so CortexDB's default of five would cut off a year of history;
// thirty-two is its ceiling, and a truncated walk says so in the result.
const chainDepth = 32

// ErrNoBrain is what the brain-only reads return when no brain was attached.
// An empty history would read as "nothing was ever decided", which is false —
// the ledger has every act; it is the brain that is missing.
var ErrNoBrain = errors.New("rollout: no brain attached — decision history lives in the brain; " +
	"`_model_ledger` still records every act (see Ledger)")

// MirrorError says an act committed, and was written to the ledger, but its
// decision did not reach the brain.
type MirrorError struct {
	Act      string
	Name     string
	Hash     string
	Decision string // the id it would have had; "" if it could not be computed
	Err      error
}

func (e *MirrorError) Error() string {
	return fmt.Sprintf("rollout: %s %s (model %s) is in the ledger but not in the brain as %s: %v — "+
		"the act stands; Resync replays the ledger into the brain", e.Act, e.Name, e.Hash, e.Decision, e.Err)
}

func (e *MirrorError) Unwrap() error { return e.Err }

// WithBrain attaches a brain: from now on every Sign, Promote and Rollback
// also records a CortexDB decision after its ledger line. Nil detaches. It
// returns the registry so it chains onto New; it is not safe to call while
// other goroutines are using the registry.
func (r *Registry) WithBrain(db *cortexdb.DB) *Registry {
	r.brain = db
	return r
}

// OnMirrorError sets who is told when a decision does not reach the brain.
// The function receives a *MirrorError. Nil restores the default, the log.
func (r *Registry) OnMirrorError(fn func(error)) *Registry {
	r.report = fn
	return r
}

// DecisionIDFor is the decision id a ledger line is mirrored under, in the
// prefixed form CortexDB reads back.
func DecisionIDFor(e Entry) string {
	return cortexdb.DecisionID("di:rollout:" + e.Act + ":" + e.Name + ":" + e.ToHash + ":" + e.At)
}

// ModelVersionNodeID is the node a decision about these bytes points at.
func ModelVersionNodeID(hash string) string { return ModelVersionNodeType + ":" + hash }

// History is what the brain says about one registered version.
type History struct {
	Name   string
	Hash   string // the hash it was signed as; the registered hash if unsigned
	Status string
	// Head is the newest mirrored decision about this version, and Chain is
	// the walk back from it: what it rested on and what it superseded, each
	// with who decided and why. Head is empty when nothing is mirrored yet.
	Head  string
	Chain cortexdb.DecisionChain
	// About is every decision whose subject is these bytes, newest first —
	// including a signature nobody has promoted yet.
	About []cortexdb.DecisionRecord
	// Unmirrored is every ledger line for this version that the brain does not
	// hold. Non-empty means the chain above is missing something the ledger
	// knows; Resync fills it in.
	Unmirrored []Entry
}

// History reads a version's decision chain back out of the brain.
func (r *Registry) History(ctx context.Context, name string) (*History, error) {
	if r.brain == nil {
		return nil, ErrNoBrain
	}
	v, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	entries, err := r.Ledger(ctx)
	if err != nil {
		return nil, err
	}
	h := &History{Name: v.Name, Hash: v.SignedHash, Status: v.Status}
	if h.Hash == "" {
		h.Hash = v.Hash
	}

	head := -1
	for i, e := range entries {
		if e.Name != name || e.ToHash == "" {
			continue
		}
		if !r.holds(ctx, DecisionIDFor(e)) {
			h.Unmirrored = append(h.Unmirrored, e)
			continue
		}
		if head < 0 || entries[head].At <= e.At {
			head = i
		}
	}
	if head < 0 {
		if len(h.Unmirrored) == 0 {
			return nil, fmt.Errorf("rollout: %s has no ledger entries — it was registered but never signed", name)
		}
		return h, nil
	}
	h.Head = DecisionIDFor(entries[head])
	if h.Chain, err = r.brain.DecisionChain(ctx, h.Head, chainDepth); err != nil {
		return nil, err
	}
	if h.About, err = r.brain.Precedents(ctx, cortexdb.PrecedentsQuery{
		Subject: ModelVersionNodeID(h.Hash), Limit: 100,
	}); err != nil {
		return nil, err
	}
	return h, nil
}

// Resync replays the ledger into the brain, writing every line the brain does
// not already hold, oldest first. It is idempotent — the ids are the ledger
// lines' own — and returns how many decisions it wrote.
func (r *Registry) Resync(ctx context.Context) (int, error) {
	if r.brain == nil {
		return 0, ErrNoBrain
	}
	entries, err := r.Ledger(ctx)
	if err != nil {
		return 0, err
	}
	written := 0
	for i, e := range entries {
		if e.ToHash == "" || r.holds(ctx, DecisionIDFor(e)) {
			continue
		}
		n, err := r.mirrorEntry(ctx, entries, i, map[int]bool{})
		written += n
		if err != nil {
			return written, &MirrorError{Act: e.Act, Name: e.Name, Hash: e.ToHash, Decision: DecisionIDFor(e), Err: err}
		}
	}
	return written, nil
}

// mirror is what Sign, Promote and Rollback call after their ledger line
// committed. It never returns an error; see the file comment for why.
func (r *Registry) mirror(ctx context.Context, act, name, hash string) {
	if r.brain == nil {
		return
	}
	fail := func(id string, err error) {
		me := &MirrorError{Act: act, Name: name, Hash: hash, Decision: id, Err: err}
		if r.report != nil {
			r.report(me)
			return
		}
		log.Print(me)
	}
	entries, err := r.Ledger(ctx)
	if err != nil {
		fail("", err)
		return
	}
	i := latest(entries, -1, func(e Entry) bool { return e.Act == act && e.Name == name && e.ToHash == hash })
	if i < 0 {
		fail("", fmt.Errorf("the ledger line was written but cannot be read back"))
		return
	}
	if _, err := r.mirrorEntry(ctx, entries, i, map[int]bool{}); err != nil {
		fail(DecisionIDFor(entries[i]), err)
	}
}

func isActivation(e Entry) bool { return e.Act == "promote" || e.Act == "rollback" }

// mirrorEntry records ledger line i, first recording any line it points at
// that the brain does not hold yet. It returns how many decisions it wrote.
func (r *Registry) mirrorEntry(ctx context.Context, entries []Entry, i int, visiting map[int]bool) (int, error) {
	e := entries[i]
	if e.ToHash == "" {
		return 0, nil
	}
	visiting[i] = true
	defer delete(visiting, i)

	basedOn, supersedes := -1, -1
	switch e.Act {
	case "sign":
	case "promote", "rollback":
		basedOn = latest(entries, i, func(x Entry) bool {
			return x.Act == "sign" && x.Name == e.Name && x.ToHash == e.ToHash
		})
		switch {
		case e.Act == "promote" && e.FromHash != "":
			supersedes = latest(entries, i, func(x Entry) bool { return isActivation(x) && x.ToHash == e.FromHash })
		case e.Act == "rollback":
			supersedes = latest(entries, i, isActivation)
		}
	default:
		// An act this file does not know how to read. Recording it with no
		// edges would put an entry in the brain that looks complete and is not.
		return 0, fmt.Errorf("unknown ledger act %q", e.Act)
	}

	written := 0
	dep := func(j int) (string, error) {
		if j < 0 || visiting[j] {
			return "", nil
		}
		id := DecisionIDFor(entries[j])
		if r.holds(ctx, id) {
			return id, nil
		}
		n, err := r.mirrorEntry(ctx, entries, j, visiting)
		written += n
		if err != nil {
			return "", fmt.Errorf("the %s it rests on (%s): %w", entries[j].Act, id, err)
		}
		return id, nil
	}
	req := cortexdb.DecisionRecordRequest{
		ID:      DecisionIDFor(e),
		Kind:    decisionKindPrefix + e.Act,
		Actor:   e.By,
		Verdict: verdicts[e.Act],
		Note:    decisionNote(e),
		Subject: ModelVersionNodeID(e.ToHash),
		Source:  decisionSource,
	}
	premise, err := dep(basedOn)
	if err != nil {
		return written, err
	}
	if premise != "" {
		req.Premises = []string{premise}
	}
	if req.Supersedes, err = dep(supersedes); err != nil {
		return written, err
	}
	// The ledger's time, not now: a replayed line must converge on the entry
	// it already wrote, and the decision happened when the ledger says it did.
	if at, err := time.Parse(time.RFC3339, e.At); err == nil {
		req.At = at
	}
	if err := r.ensureModelNode(ctx, e.ToHash); err != nil {
		return written, err
	}
	if _, err := r.brain.RecordDecision(ctx, req); err != nil {
		return written, err
	}
	return written + 1, nil
}

var verdicts = map[string]string{"sign": "signed", "promote": "promoted", "rollback": "restored"}

// latest is the newest ledger line matching pred, other than skip, and no newer
// than skip's own line. Newest by timestamp, then by position: the ledger
// is read ORDER BY ts and a timestamp has one-second resolution, so two acts
// scripted back to back share one, and position is the best tie-break there is.
func latest(entries []Entry, skip int, pred func(Entry) bool) int {
	best := -1
	for j, x := range entries {
		if j == skip || !pred(x) {
			continue
		}
		if skip >= 0 && x.At > entries[skip].At {
			continue
		}
		if best < 0 || entries[best].At <= x.At {
			best = j
		}
	}
	return best
}

// decisionNote is the entry in words, then the ledger line itself as JSON.
// Go sorts map keys when it marshals, so the same line renders the same bytes
// every time, which is what lets a replay converge instead of churning.
func decisionNote(e Entry) string {
	var line string
	switch e.Act {
	case "sign":
		line = fmt.Sprintf("%s signed %s (model %s)", e.By, e.Name, e.ToHash)
	case "promote":
		line = fmt.Sprintf("%s (model %s) went live on %s's signature", e.Name, e.ToHash, e.By)
		if e.FromHash != "" {
			// Promoting bytes that were already live under another registry
			// name replaces a version, not a definition. "replacing model X"
			// with X the model itself reads as a version replacing itself,
			// which is what it printed on a real registry.
			if e.FromHash == e.ToHash {
				line += ", replacing a version with identical bytes — no definition changed"
			} else {
				line += fmt.Sprintf(", replacing model %s", e.FromHash)
			}
		}
		if e.Changed != "" {
			line += "; metrics redefined: " + e.Changed
		}
	case "rollback":
		line = fmt.Sprintf("%s (model %s) restored by rollback, on %s's original signature", e.Name, e.ToHash, e.By)
	}
	if n := strings.TrimSpace(e.Note); n != "" && e.Act != "rollback" {
		line += " — " + n
	}
	detail := map[string]string{"act": e.Act, "name": e.Name, "model": e.ToHash, "ledger_at": e.At}
	if e.FromHash != "" {
		detail["from"] = e.FromHash
	}
	if e.Changed != "" {
		detail["changed"] = e.Changed
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return line
	}
	return line + "\n" + string(body)
}

func (r *Registry) holds(ctx context.Context, id string) bool {
	_, err := r.brain.Graph().GetNode(ctx, id)
	return err == nil
}

// ensureModelNode writes the node a decision is about, once. Written only when
// absent, so a later decision about the same bytes does not open a new version
// of a node whose content has not changed.
//
// Its vector is a placeholder derived from the id, as modelgraph's are: the
// store requires one, and nothing searches model versions by similarity.
func (r *Registry) ensureModelNode(ctx context.Context, hash string) error {
	id := ModelVersionNodeID(hash)
	if r.holds(ctx, id) {
		return nil
	}
	return r.brain.Graph().UpsertNode(ctx, &graph.GraphNode{
		ID:         id,
		NodeType:   ModelVersionNodeType,
		Content:    "semantic model " + hash,
		Properties: map[string]any{"model_hash": hash},
		Vector:     placeholderVector(id, r.brain.Info().Dimensions),
	})
}

func placeholderVector(id string, dim int) []float32 {
	if dim < 1 {
		dim = 1
	}
	v := make([]float32, dim)
	h := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		h = (h ^ uint32(id[i])) * 16777619
	}
	for i := range v {
		h = h*1664525 + 1013904223
		v[i] = float32(h%2000)/1000 - 1
	}
	return v
}
