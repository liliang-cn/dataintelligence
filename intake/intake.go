// Package intake is the signed answer to "who said we could read this".
//
// Before DI models a customer's database it reads it: the catalogue first,
// then counts, samples, distinct values, and — with `model gen -llm` — enough
// of all that to hand to a model. Nothing recorded that anybody at the
// customer agreed to any of it, or which columns were agreed to be personal.
// The rollout registry answers "who approved this definition of the number";
// nothing answered "who approved us reading the phone column to build it".
//
// The shape is athanor's pkg/livedb, and so is the load-bearing part — the
// signature binds to a hash of the plan rather than to its id:
//
//  1. Propose reads the catalogue, and only the catalogue (modelgen.Catalogue:
//     no row, no sample, no count), and writes a DRAFT with one treatment per
//     column. A column modelgen.PIIColumn calls personal starts at mask;
//     everything else starts at keep.
//  2. Amend is a person overriding the classifier. Every amendment that
//     changes a treatment changes the hash, so a signature over the old hash
//     cannot be carried across the change.
//  3. Sign names the hash the signer read. A hash that is not the plan's
//     current one is refused: they would be approving something they did not
//     see.
//  4. Require is the gate a command calls before it reads the customer's
//     rows. No signed plan is a refusal; a column in the live catalogue that
//     the signed plan does not classify is a refusal naming the column,
//     because it is exactly the column nobody decided about.
//
// # Why not import pkg/livedb
//
// The owner's direction is to import athanor packages rather than rewrite
// them, and livedb was read with that intent. It cannot be used cleanly here,
// for four reasons, any one of which would be enough:
//
//   - Propose dials the database itself, through CortexDB's connector, and
//     validateSource refuses every driver but postgres and mysql before the
//     injectable Opener is even consulted. DI's warehouses include SQLite and
//     SQL Server, and DI already has the one connection it should be reading
//     through; a second driver path to the customer's database is the thing
//     this package exists to govern, not a thing it should add.
//   - Its classifier is connector.NewRuleClassifier and its proposal samples
//     rows to show the reviewer. DI already has a PII rule that its generated
//     models mask by, and a proposal here must not read a row at all.
//   - Its drift check (driftOf) is unexported and runs only inside Run, which
//     is the database → graph import. That import lost to plain SQL — fifty
//     milliseconds against an hour on the same question — and is exactly what
//     must not come across. There is no exported "is the live schema still
//     what was signed" to call without it.
//   - livedb.New wires importflow in unconditionally, so importing the
//     package links the row-import path whether or not it is called.
//
// What is kept is the design, deliberately: the canonical hash over a struct
// marshalled with encoding/json, the refusal to sign a hash that is not the
// current one, a signed plan that is superseded rather than edited, a partial
// unique index that makes "one plan in force per source" a property of the
// table, and the source identified by a DSN with its credential removed.
//
// # Where it is kept
//
// On the brain — the cortexdb.DB platform.Open already holds — through its
// SQL() handle and Dialect(), in tables prefixed di_intake_. Not in the
// customer's warehouse: DI treats that as read-only, and a record of what DI
// was allowed to read has no business living in the thing it governs, where
// the customer's own DBA can edit it and a restore of their backup would
// roll it back. On the brain it travels with the rest of what DI has written
// down, and a backup of one file carries it.
package intake

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Errors a caller can tell apart. Values rather than strings, so a command
// can print the next step for each without matching on a message somebody
// will one day reword.
var (
	// ErrNoPlan is a plan id nothing answers to.
	ErrNoPlan = errors.New("intake: no such plan")
	// ErrUnsigned is a gate asked about a source nobody has signed for.
	ErrUnsigned = errors.New("intake: no signed intake plan")
	// ErrNotDraft is an amendment or signature on a plan already signed or
	// superseded. A signed plan is never edited; a new one supersedes it.
	ErrNotDraft = errors.New("intake: a signed plan is not edited, it is superseded")
	// ErrStaleHash is a signature naming a hash the plan no longer has.
	ErrStaleHash = errors.New("intake: the plan changed since you read it")
	// ErrTampered is a stored plan whose treatments no longer hash to the
	// hash stored beside them: the row was changed without going through
	// Amend, so no signature can be said to cover it.
	ErrTampered = errors.New("intake: the stored plan does not hash to its recorded hash")
	// ErrDrift is a live catalogue holding a column or table the signed plan
	// does not classify.
	ErrDrift = errors.New("intake: the database has columns nobody classified")
)

// Action is what may be done with a column's values.
//
// The spellings are CortexDB connector.MaskAction's, so a plan read by a
// person who knows livedb means the same thing; the meanings are DI's, which
// reads through SQL rather than streaming rows through a desensitizer.
type Action string

const (
	// Keep: the values may be read and shown as they are.
	Keep Action = "keep"
	// Mask: the values may be read to aggregate, filter and join on, and are
	// never shown or sent anywhere unmasked — the column's dimension is
	// generated with a mask, and no sample of it goes to a model.
	Mask Action = "mask"
	// Redact: the column is not read at all — not selected, not sampled, not
	// named to a model beyond the fact that it exists.
	Redact Action = "redact"
)

func (a Action) valid() bool { return a == Keep || a == Mask || a == Redact }

// State is where a plan is in its life.
type State string

const (
	// Draft is proposed, amendable, and gates nothing.
	Draft State = "draft"
	// Signed is the one plan in force for its source.
	Signed State = "signed"
	// Superseded is a plan a later signature replaced. It is kept: whatever
	// was read under it is only explicable while it still exists.
	Superseded State = "superseded"
)

// Treatment is one column's row on the plan.
type Treatment struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	// Type is the declared type from the catalogue.
	Type string `json:"type,omitempty"`
	// Personal is the classifier's call, kept after an amendment so a reader
	// can see where a person disagreed with it.
	Personal bool   `json:"personal"`
	Action   Action `json:"action"`
	// Reason and By are provenance: why this action, and who chose it
	// ("rule" for the classifier). Outside the hash — who said so is not what
	// was decided.
	Reason string `json:"reason,omitempty"`
	By     string `json:"by,omitempty"`
}

// Plan is the reviewable decision: which columns of which tables DI may read,
// and how.
type Plan struct {
	ID string `json:"id"`
	// Source is the database's identity: its DSN with the credential removed
	// (see SourceOf). Plans over the same Source supersede one another.
	Source string `json:"source"`
	// Driver is the warehouse driver the catalogue was read through.
	Driver  string      `json:"driver"`
	Columns []Treatment `json:"columns"`
	// Hash is sha256 over the canonical rendering of Source, Driver and the
	// decisions. It is what a signature names.
	Hash  string `json:"hash"`
	State State  `json:"state"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SignedBy  string    `json:"signed_by,omitempty"`
	SignedAt  time.Time `json:"signed_at,omitzero"`
	// Supersedes is the plan this one replaced when it was signed.
	Supersedes string `json:"supersedes,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Treatment returns the plan's decision for one column.
func (p Plan) Treatment(table, column string) (Treatment, bool) {
	for _, t := range p.Columns {
		if t.Table == table && t.Column == column {
			return t, true
		}
	}
	return Treatment{}, false
}

// Short is the first twelve hex digits of the hash — what a CLI prints and
// what a signer may type back.
func (p Plan) Short() string { return short(p.Hash) }

// Change is one amendment. An empty Column applies Action to every column of
// Table, which is how a table is taken out of scope: redact all of it.
type Change struct {
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// Report is what the live catalogue says against the signed plan.
type Report struct {
	Plan Plan `json:"plan"`
	// Unclassified names table.column pairs the database has and the plan
	// does not — a whole new table shows as each of its columns. Non-empty
	// is a refusal from Require.
	Unclassified []string `json:"unclassified,omitempty"`
	// Gone names columns the plan classifies and the database no longer has.
	// Reported, not refused: a column that is not there cannot be read.
	Gone []string `json:"gone,omitempty"`
	// Retyped names columns whose declared type changed since signing, as
	// "table.column old→new". Reported, not refused: the treatment was
	// decided by what the column is called and holds, and a varchar widened
	// to text is not a new decision — but a reader should see it.
	Retyped []string `json:"retyped,omitempty"`
}

// Entry is one line of the intake ledger: every act, in order, with the hash
// before and after it.
type Entry struct {
	At       time.Time `json:"at"`
	Act      string    `json:"act"` // propose | amend | sign | supersede
	Plan     string    `json:"plan"`
	Source   string    `json:"source"`
	By       string    `json:"by"`
	FromHash string    `json:"from_hash,omitempty"`
	ToHash   string    `json:"to_hash"`
	Note     string    `json:"note,omitempty"`
}

// SourceOf is the stable identity of a database: its DSN with every
// credential removed and its query string dropped.
//
// The credential goes because the plan is kept, shown and backed up, and a
// brain file that leaks should leak a hostname rather than a login — livedb's
// rule. The query string goes because `sslmode=disable` against `require`, or
// SQLite's `mode=ro` against `mode=rwc`, is the same database, and a gate that
// treated them as two would demand a second signature for nothing.
func SourceOf(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	// MySQL's user:pass@tcp(host)/db is not a URL; take the password out of
	// it before url.Parse gets a chance to misread it.
	if !strings.Contains(dsn, "://") {
		if m := mysqlCred.FindStringSubmatchIndex(dsn); m != nil {
			dsn = dsn[:m[2]] + dsn[m[3]:]
		}
		dsn = keywordPass.ReplaceAllString(dsn, "")
		if i := strings.IndexByte(dsn, '?'); i >= 0 {
			dsn = dsn[:i]
		}
		return strings.Join(strings.Fields(dsn), " ")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		// Unparseable is not a reason to keep the secret: drop everything
		// between the scheme and the host.
		if at := strings.LastIndexByte(dsn, '@'); at >= 0 {
			if s := strings.Index(dsn, "://"); s >= 0 && s < at {
				return dsn[:s+3] + dsn[at+1:]
			}
		}
		return dsn
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

var (
	// user:pass@ at the front of a MySQL DSN; group 1 is ":pass".
	mysqlCred = regexp.MustCompile(`^[^:@/\s]+(:[^@]*)@`)
	// password=… in a keyword DSN, quoted or not.
	keywordPass = regexp.MustCompile(`(?i)\b(password|passwd|pwd)\s*=\s*('[^']*'|\S+)`)
)

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
