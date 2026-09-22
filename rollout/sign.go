package rollout

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Who approved this definition of the number, and when.
//
// The registry could already register a model, canary it and promote it, and
// none of those steps asked for a person. That is the whole change-management
// plane working correctly and answering none of the questions anyone actually
// asks after a number moves: who changed the definition, when, and did anybody
// agree to it. `_audit` records the questions people ask; nothing recorded the
// changes to what the answers mean.
//
// The shape here is the one the live-database plane uses for a privacy plan —
// propose, read, sign a hash, then run — and the load-bearing part is that the
// signature binds to a hash rather than to a name:
//
//   - Register hashes the file. Promote then re-read the file from disk. Those
//     are two different reads of a mutable path, and nothing checked that they
//     agreed, so editing the YAML after registering and before promoting put a
//     model into production that was never registered. The hash in the registry
//     still named the old one. TestPromoteRefusesAModelEditedAfterItWasSigned
//     is that sequence.
//   - A signer passes back the hash they read. If it does not match what is on
//     disk now, they are approving something they did not see, and the answer
//     is a refusal rather than a warning.
//
// SignedHash is therefore stored beside Hash instead of being assumed equal to
// it. They are equal at the moment of signing and diverge the instant somebody
// edits the file, and that divergence is exactly the thing worth refusing on.

// Sign records that `by` approved the model file behind `name`, having read the
// version whose hash is `hash`.
//
// It refuses rather than warns in three cases, all of which mean the signature
// would say something untrue:
//
//   - no signer: an approval by nobody is not an approval
//   - the file on disk no longer hashes to what Register stored: the registered
//     version is gone, and re-registering is the honest way back
//   - the signer read a different hash: they are approving a model they have
//     not seen
func (r *Registry) Sign(ctx context.Context, name, hash, by, note string) (*Version, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, fmt.Errorf("rollout: a signature needs a signer — pass who is approving %q", name)
	}
	v, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	onDisk, err := HashFile(v.Path)
	if err != nil {
		return nil, fmt.Errorf("rollout: re-read %s: %w", v.Path, err)
	}
	if onDisk != v.Hash {
		return nil, fmt.Errorf(
			"rollout: %s has changed on disk since it was registered (registered %s, now %s) — "+
				"register the new file rather than signing over the old record", v.Path, v.Hash, onDisk)
	}
	if h := strings.TrimSpace(hash); h != "" && h != onDisk {
		return nil, fmt.Errorf(
			"rollout: you signed %s but %s is %s — you would be approving a model you have not read",
			h, v.Path, onDisk)
	}
	v.SignedHash, v.SignedBy, v.SignedAt, v.SignNote = onDisk, by, r.nowStr(), note
	if err := r.save(ctx, v); err != nil {
		return nil, err
	}
	return v, r.record(ctx, "sign", v.Name, by, "", onDisk, "", note)
}

// requireSignature is the gate Promote runs before making anything live.
func (r *Registry) requireSignature(v *Version) error {
	if v.SignedHash == "" {
		return fmt.Errorf(
			"rollout: %s has not been signed — `di rollout sign -name %s -by <who>` before promoting it",
			v.Name, v.Name)
	}
	onDisk, err := HashFile(v.Path)
	if err != nil {
		return fmt.Errorf("rollout: re-read %s: %w", v.Path, err)
	}
	if onDisk != v.SignedHash {
		return fmt.Errorf(
			"rollout: %s was signed as %s by %s, but %s is now %s — the file changed after it was approved; "+
				"register and sign the new one", v.Name, v.SignedHash, v.SignedBy, v.Path, onDisk)
	}
	return nil
}

// Entry is one line of the change ledger.
type Entry struct {
	At       string
	Act      string
	Name     string
	By       string
	FromHash string
	ToHash   string
	Changed  string
	Note     string
}

var ledgerDDL = map[string]string{
	"pgx":       `CREATE TABLE IF NOT EXISTS _model_ledger (ts text, act text, name text, "by" text, from_hash text, to_hash text, changed text, note text)`,
	"sqlite":    `CREATE TABLE IF NOT EXISTS _model_ledger (ts TEXT, act TEXT, name TEXT, "by" TEXT, from_hash TEXT, to_hash TEXT, changed TEXT, note TEXT)`,
	"duckdb":    `CREATE TABLE IF NOT EXISTS _model_ledger (ts VARCHAR, act VARCHAR, name VARCHAR, "by" VARCHAR, from_hash VARCHAR, to_hash VARCHAR, changed VARCHAR, note VARCHAR)`,
	"mysql":     "CREATE TABLE IF NOT EXISTS _model_ledger (ts VARCHAR(40), act VARCHAR(20), name VARCHAR(190), `by` VARCHAR(190), from_hash VARCHAR(64), to_hash VARCHAR(64), changed TEXT, note TEXT)",
	"sqlserver": `IF OBJECT_ID('_model_ledger','U') IS NULL CREATE TABLE _model_ledger (ts NVARCHAR(40), act NVARCHAR(20), name NVARCHAR(190), [by] NVARCHAR(190), from_hash NVARCHAR(64), to_hash NVARCHAR(64), changed NVARCHAR(MAX), note NVARCHAR(MAX))`,
}

func (r *Registry) ensureLedger(ctx context.Context) error {
	ddl, ok := ledgerDDL[r.wh.Driver()]
	if !ok {
		return fmt.Errorf("rollout: no ledger schema for driver %q", r.wh.Driver())
	}
	_, err := r.wh.Exec(ctx, ddl)
	return err
}

// record appends one line. It is the last thing every state change does, and it
// returns its error rather than swallowing it: a promotion that happened and
// was not written down is the case this package exists to prevent, so the
// caller is told, even though the promotion itself already committed.
func (r *Registry) record(ctx context.Context, act, name, by, from, to, changed, note string) error {
	cols := []string{"ts", "act", "name", "by", "from_hash", "to_hash", "changed", "note"}
	d := r.wh.Dialect()
	quoted := make([]string, len(cols))
	holders := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = d.QuoteIdent(c)
		holders[i] = d.Placeholder(i + 1)
	}
	_, err := r.wh.Exec(ctx,
		fmt.Sprintf("INSERT INTO _model_ledger (%s) VALUES (%s)",
			strings.Join(quoted, ", "), strings.Join(holders, ", ")),
		r.nowStr(), act, name, by, from, to, changed, note)
	if err != nil {
		return fmt.Errorf("rollout: %s %s happened but was not written to the ledger: %w", act, name, err)
	}
	return nil
}

// Ledger reads the change history back, newest last — the answer to "when did
// this number's definition change, and who said yes".
func (r *Registry) Ledger(ctx context.Context) ([]Entry, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	d := r.wh.Dialect()
	res, err := r.wh.Query(ctx, fmt.Sprintf(
		"SELECT ts, act, name, %s, from_hash, to_hash, changed, note FROM _model_ledger ORDER BY ts",
		d.QuoteIdent("by")))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(res.Rows))
	for _, row := range res.Rows {
		f := make([]string, 8)
		for i := range f {
			f[i] = text(row[i])
		}
		out = append(out, Entry{At: f[0], Act: f[1], Name: f[2], By: f[3],
			FromHash: f[4], ToHash: f[5], Changed: f[6], Note: f[7]})
	}
	return out, nil
}

// text flattens whatever the driver hands back for a text column. sqlite gives
// []byte where pgx gives string, and a nil is an empty cell rather than an
// error — a ledger row with no note is ordinary.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case sql.NullString:
		return t.String
	default:
		return fmt.Sprint(t)
	}
}
