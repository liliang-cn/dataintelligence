package rollout

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Which answers came from a definition somebody approved.
//
// Two tables were keeping two halves of one record. `_audit` knows who asked
// what and what SQL ran; `_model_ledger` knows who approved which definition of
// the numbers. Neither could reach the other, so the question a reader of
// either one actually has — "this figure: whose definition of revenue is it,
// and did anyone sign off on that" — had no answer in the product at all.
//
// The join is the model hash, stamped on every trail row by the engine that
// produced the answer. It is a hash rather than a version name on purpose: a
// name says which row of the registry was current, and a hash says which bytes
// were compiled. Those differ exactly when somebody edited a model file without
// going through the registry, which is the case worth catching.
//
// Attest deliberately reports rather than refuses. An unattested answer is not
// necessarily wrong — a deployment that predates the registry has a trail full
// of them, and so does any run against a model handed over in memory. What it
// is, is unaccounted for, and the difference between "approved by 张三 on the
// 14th" and "nobody knows" is the whole product.

// HashFile is the registry's hash of a model file: the first twelve hex digits
// of its SHA-256. Exported because the engine stamps the same hash onto every
// audit row, and the two computing it differently would break the join in a way
// no error message would ever mention.
func HashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// HashBytes is HashFile for content already in hand.
func HashBytes(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b))[:12] }

// Attestation is what the trail and the ledger say together about one model.
type Attestation struct {
	Hash     string // the model that produced the answers; "" = no model recorded
	Answers  int    // how many rows of the trail it produced
	SignedBy string // who approved it, empty if nobody did
	SignedAt string
	Note     string
	Promoted bool // whether it was ever made active through the registry

	// ApprovedAt is when these bytes first went live through the registry,
	// and BeforeApproval is how many of the answers were given before that.
	//
	// The join is on the hash, and a hash signed today matches every answer
	// those bytes ever produced — so without the time, a signature given this
	// afternoon approved figures sent last month. On a real trail that read
	// "125 of 125 answers came from an approved definition" about 125 answers
	// that were all given before anybody signed. An audit asks whether a
	// figure was approved when it was sent; "it is approved now" is a
	// different answer and has to be reported as one.
	ApprovedAt     string
	BeforeApproval int
}

// Attest reads the trail back and reports, per model hash, how many answers it
// produced and who — if anyone — signed it.
//
// Rows are ordered by answer count, busiest first: an unsigned model that
// answered one question is a curiosity, and an unsigned model that answered
// four thousand is an incident.
func (r *Registry) Attest(ctx context.Context) ([]Attestation, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	when := map[string][]answerTime{}
	res, err := r.wh.Query(ctx, "SELECT model_hash, ts FROM _audit")
	if err != nil {
		// No trail is not an error: nobody has asked anything yet. A missing
		// model_hash column is the same case seen from an older deployment.
		return nil, fmt.Errorf("no audit trail to read (%w) — has anyone asked a question yet?", err)
	}
	for _, row := range res.Rows {
		h := text(row[0])
		counts[h]++
		t, ok := parseWhen(row[1])
		when[h] = append(when[h], answerTime{t: t, ok: ok})
	}

	signed := map[string]Attestation{}
	entries, err := r.Ledger(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.ToHash == "" {
			continue
		}
		a := signed[e.ToHash]
		a.Hash = e.ToHash
		switch e.Act {
		case "sign":
			a.SignedBy, a.SignedAt, a.Note = e.By, e.At, e.Note
		case "promote", "rollback":
			a.Promoted = true
			if a.SignedBy == "" {
				a.SignedBy, a.SignedAt = e.By, e.At
			}
			if a.ApprovedAt == "" || e.At < a.ApprovedAt {
				a.ApprovedAt = e.At
			}
		}
		signed[e.ToHash] = a
	}

	out := make([]Attestation, 0, len(counts))
	for h, n := range counts {
		a := signed[h]
		a.Hash, a.Answers = h, n
		if a.Promoted {
			a.BeforeApproval = countBefore(when[h], a.ApprovedAt)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Answers != out[j].Answers {
			return out[i].Answers > out[j].Answers
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// Signature is what the ledger says about one model hash, without going
// through the trail: the question a single answer asks about itself.
//
// The second return is whether the ledger has heard of this hash at all. A
// false there is the honest "nobody approved this", and it is deliberately not
// an error: running against an unregistered model is a normal thing to do while
// developing one, and only becomes a finding when it happens in front of a
// customer.
func (r *Registry) Signature(ctx context.Context, hash string) (Attestation, bool, error) {
	if hash == "" {
		return Attestation{}, false, nil
	}
	entries, err := r.Ledger(ctx)
	if err != nil {
		return Attestation{}, false, err
	}
	a, found := Attestation{Hash: hash}, false
	for _, e := range entries {
		if e.ToHash != hash {
			continue
		}
		found = true
		switch e.Act {
		case "sign":
			a.SignedBy, a.SignedAt, a.Note = e.By, e.At, e.Note
		case "promote", "rollback":
			a.Promoted = true
			if a.SignedBy == "" {
				a.SignedBy, a.SignedAt = e.By, e.At
			}
		}
	}
	return a, found, nil
}

// Unattested is the subset of Attest worth acting on: answers produced by a
// model that was never promoted through the registry.
func Unattested(all []Attestation) []Attestation {
	var out []Attestation
	for _, a := range all {
		if !a.Promoted {
			out = append(out, a)
		}
	}
	return out
}

type answerTime struct {
	t  time.Time
	ok bool
}

// countBefore counts answers given before approval. An answer whose time
// cannot be read is counted as before: the report is about what can be shown
// to have been approved, and an unreadable timestamp shows nothing.
func countBefore(ts []answerTime, approvedAt string) int {
	at, err := time.Parse(time.RFC3339, approvedAt)
	if err != nil {
		return len(ts)
	}
	n := 0
	for _, a := range ts {
		if !a.ok || a.t.Before(at) {
			n++
		}
	}
	return n
}

// parseWhen reads the trail's timestamp however the engine returned it:
// Postgres hands back a time.Time, SQLite the text of datetime('now') in UTC,
// MySQL either, depending on the driver's parseTime setting.
func parseWhen(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case []byte:
		return parseWhen(string(t))
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339,
			"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07",
			"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
			if p, err := time.Parse(layout, strings.TrimSpace(t)); err == nil {
				return p, true
			}
		}
	}
	return time.Time{}, false
}
