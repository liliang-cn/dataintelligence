package writeback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Principal is the caller identity for the write path (kept local to avoid an
// import cycle; the CLI maps the governance principal onto it).
type Principal struct {
	User string
	Role string
}

// Engine drives the propose → approve → commit → rollback lifecycle.
type Engine struct {
	WH        *warehouse.Warehouse
	Schema    *Schema
	ModelPath string       // for transform (model-change) proposals
	NowNs     func() int64 // injected clock (deterministic-friendly)
}

// Propose validates + dry-runs a change and persists it as pending. Nothing is
// applied. The proposer must be allowed to propose.
func (e *Engine) Propose(ctx context.Context, p Principal, prop *Proposal) (*Proposal, error) {
	if !e.Schema.CanPropose(p.Role) {
		return nil, fmt.Errorf("role %q may not propose writes", p.Role)
	}
	prop.Proposer = p.User + "/" + p.Role
	prop.Status = StatusPending
	prop.CreatedNs = e.now()
	prop.ID = fmt.Sprintf("prop-%d", prop.CreatedNs)

	switch prop.Kind {
	case "data":
		pv, err := Plan(ctx, e.WH, e.Schema, prop.Data)
		if err != nil {
			return nil, err
		}
		prop.Preview = pv
	case "model":
		if err := e.planModel(prop); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown proposal kind %q", prop.Kind)
	}
	if err := e.save(ctx, prop); err != nil {
		return nil, err
	}
	return prop, nil
}

// Approve commits a pending proposal. The approver must be allowed to approve AND
// must be a different principal than the proposer (separation of duties).
func (e *Engine) Approve(ctx context.Context, p Principal, id string) (*Proposal, error) {
	prop, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if prop.Status != StatusPending {
		return nil, fmt.Errorf("proposal %s is %s, not pending", id, prop.Status)
	}
	if !e.Schema.CanApprove(p.Role) {
		return nil, fmt.Errorf("role %q may not approve writes", p.Role)
	}
	approver := p.User + "/" + p.Role
	if approver == prop.Proposer {
		return nil, fmt.Errorf("separation of duties: the proposer (%s) cannot approve their own change", prop.Proposer)
	}
	prop.Approver = approver
	prop.DecidedNs = e.now()

	if prop.Kind == "model" {
		if err := e.commitModel(prop); err != nil {
			return nil, err
		}
	} else if err := e.commitData(ctx, prop); err != nil {
		return nil, err
	}
	prop.Status = StatusCommitted
	prop.CommittedNs = e.now()
	if err := e.save(ctx, prop); err != nil {
		return nil, err
	}
	e.audit(ctx, prop, "commit")
	return prop, nil
}

// Reject marks a pending proposal rejected (nothing was applied).
func (e *Engine) Reject(ctx context.Context, p Principal, id string) (*Proposal, error) {
	prop, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if prop.Status != StatusPending {
		return nil, fmt.Errorf("proposal %s is %s, not pending", id, prop.Status)
	}
	prop.Status = StatusRejected
	prop.Approver = p.User + "/" + p.Role
	prop.DecidedNs = e.now()
	if err := e.save(ctx, prop); err != nil {
		return nil, err
	}
	e.audit(ctx, prop, "reject")
	return prop, nil
}

// commitData snapshots the before-image and applies the mutation in ONE tx, then
// records the proposal as committed (with the snapshot) — all atomic.
func (e *Engine) commitData(ctx context.Context, prop *Proposal) error {
	d := prop.Data
	if err := d.Validate(e.Schema); err != nil {
		return err
	}
	sqlStr, args, err := build(d)
	if err != nil {
		return err
	}
	if v := redLineViolation(sqlStr); v != "" {
		return fmt.Errorf("%s", v)
	}
	t := e.Schema.Table(d.Table)

	return e.WH.Apply(ctx, func(tx *sql.Tx) error {
		// Re-snapshot the before-image inside the tx (consistent with the mutation).
		if d.Op == OpUpdate || d.Op == OpDelete {
			selSQL, selArgs, err := buildSelect(d)
			if err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, selSQL, selArgs...)
			if err != nil {
				return err
			}
			before, err := scanMaps(rows)
			rows.Close()
			if err != nil {
				return err
			}
			if t.MaxAffected > 0 && len(before) > t.MaxAffected {
				return fmt.Errorf("would affect %d rows, over the %d cap", len(before), t.MaxAffected)
			}
			prop.Preview.Before = before
			prop.Preview.AffectedRows = len(before)
		}
		res, err := tx.ExecContext(ctx, sqlStr, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		prop.Note = fmt.Sprintf("%s affected %d row(s)", d.Op, n)
		return nil
	})
}

// Rollback reverses a committed data change from its before-image. Updates and
// deletes restore the captured rows; an insert is removed by primary key.
func (e *Engine) Rollback(ctx context.Context, p Principal, id string) (*Proposal, error) {
	prop, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if prop.Status != StatusCommitted {
		return nil, fmt.Errorf("proposal %s is %s, only a committed change can be rolled back", id, prop.Status)
	}
	if prop.Kind != "data" {
		return nil, fmt.Errorf("rollback of %s proposals is via the model backup, not here", prop.Kind)
	}
	if !e.Schema.CanApprove(p.Role) {
		return nil, fmt.Errorf("role %q may not roll back writes", p.Role)
	}
	d := prop.Data
	t := e.Schema.Table(d.Table)

	err = e.WH.Apply(ctx, func(tx *sql.Tx) error {
		switch d.Op {
		case OpInsert:
			pk := t.PrimaryKey
			val, ok := d.Set[pk]
			if !ok {
				return fmt.Errorf("cannot undo insert: no primary key %q in the proposal", pk)
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`, quoteIdent(d.Table), quoteIdent(pk)), val)
			return err
		case OpUpdate:
			for _, row := range prop.Preview.Before {
				if err := restoreRow(ctx, tx, t, d, row); err != nil {
					return err
				}
			}
		case OpDelete:
			for _, row := range prop.Preview.Before {
				if err := reinsertRow(ctx, tx, t, row); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	prop.Status = StatusRolledBack
	prop.Note = "rolled back from before-image"
	if err := e.save(ctx, prop); err != nil {
		return nil, err
	}
	e.audit(ctx, prop, "rollback")
	return prop, nil
}

// restoreRow puts the SET columns back to their before values, keyed by PK.
func restoreRow(ctx context.Context, tx *sql.Tx, t *WritableTable, d *DataChange, before map[string]any) error {
	var cols []string
	for c := range d.Set {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	sets := make([]string, len(cols))
	args := make([]any, 0, len(cols)+1)
	for i, c := range cols {
		sets[i] = fmt.Sprintf("%s = $%d", quoteIdent(c), i+1)
		args = append(args, before[c])
	}
	args = append(args, before[t.PrimaryKey])
	sql := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d`, quoteIdent(t.Name), strings.Join(sets, ", "), quoteIdent(t.PrimaryKey), len(cols)+1)
	_, err := tx.ExecContext(ctx, sql, args...)
	return err
}

// reinsertRow re-creates a deleted row from its before-image (all columns).
func reinsertRow(ctx context.Context, tx *sql.Tx, t *WritableTable, before map[string]any) error {
	var cols []string
	for c := range before {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	ph := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = before[c]
	}
	sql := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, quoteIdent(t.Name), strings.Join(quoteAll(cols), ", "), strings.Join(ph, ", "))
	_, err := tx.ExecContext(ctx, sql, args...)
	return err
}

// --- persistence + audit ---

func (e *Engine) now() int64 {
	if e.NowNs != nil {
		return e.NowNs()
	}
	return nowNs()
}

func (e *Engine) save(ctx context.Context, prop *Proposal) error {
	if _, err := e.WH.Exec(ctx, `CREATE TABLE IF NOT EXISTS _proposals (
		id text PRIMARY KEY, kind text, status text, proposer text, approver text,
		doc jsonb, at timestamptz DEFAULT now())`); err != nil {
		return err
	}
	doc, err := json.Marshal(prop)
	if err != nil {
		return err
	}
	_, err = e.WH.Exec(ctx, `INSERT INTO _proposals (id,kind,status,proposer,approver,doc) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET status=$3, approver=$5, doc=$6`,
		prop.ID, prop.Kind, string(prop.Status), prop.Proposer, prop.Approver, string(doc))
	return err
}

func (e *Engine) audit(ctx context.Context, prop *Proposal, action string) {
	_, _ = e.WH.Exec(ctx, `CREATE TABLE IF NOT EXISTS _writeback_audit (
		ts timestamptz DEFAULT now(), proposal text, action text, kind text,
		proposer text, approver text, summary text)`)
	_, _ = e.WH.Exec(ctx,
		`INSERT INTO _writeback_audit (proposal,action,kind,proposer,approver,summary) VALUES ($1,$2,$3,$4,$5,$6)`,
		prop.ID, action, prop.Kind, prop.Proposer, prop.Approver, prop.summary())
}

// Get / List for the CLI + API.
func (e *Engine) Get(ctx context.Context, id string) (*Proposal, error) {
	res, err := e.WH.Query(ctx, `SELECT doc FROM _proposals WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("proposal %s not found", id)
	}
	return decodeProposal(res.Rows[0][0])
}

func (e *Engine) List(ctx context.Context, limit int) ([]*Proposal, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := e.WH.Query(ctx, fmt.Sprintf(`SELECT doc FROM _proposals ORDER BY at DESC LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	var out []*Proposal
	for _, row := range res.Rows {
		p, err := decodeProposal(row[0])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func decodeProposal(v any) (*Proposal, error) {
	var b []byte
	switch t := v.(type) {
	case []byte:
		b = t
	case string:
		b = []byte(t)
	default:
		return nil, fmt.Errorf("unexpected doc type %T", v)
	}
	var p Proposal
	return &p, json.Unmarshal(b, &p)
}

func (p *Proposal) summary() string {
	if p.Kind == "model" && p.Model != nil {
		return fmt.Sprintf("model %s %s", p.Model.Kind, p.Model.Name)
	}
	if p.Data != nil {
		return fmt.Sprintf("%s %s (%d rows)", p.Data.Op, p.Data.Table, func() int {
			if p.Preview != nil {
				return p.Preview.AffectedRows
			}
			return 0
		}())
	}
	return p.Question
}
