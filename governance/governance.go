// Package governance enforces policy at the query boundary:
// metric RBAC (you can't even name a metric you're not authorized for), column
// masking on the result, and an append-only audit trail. Policy lives here, not
// in the prompt.
package governance

import (
	"context"
	"fmt"
	"time"

	semantic "github.com/liliang-cn/semantic-go"
	"go.opentelemetry.io/otel/attribute"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/obs"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Principal is the caller identity (in production this is the propagated user).
// Attrs carries attributes used for row-level security (e.g. region=West).
type Principal struct {
	User  string
	Role  string
	Attrs map[string]string
}

// RowFilter is a row-level-security rule: for callers in Roles, scope the query
// to rows where Dimension == the caller's Attrs[AttrKey] (bound to the live
// security context, never a literal).
type RowFilter struct {
	Dimension string
	AttrKey   string
	Roles     []string
}

// Policy controls masking, row-level security, and k-anonymity.
type Policy struct {
	Unmask      map[string]bool // roles allowed to see masked dimensions raw
	RowFilters  []RowFilter     // RLS rules
	K           int             // k-anonymity threshold (0 = off)
	KAnonDims   []string        // dimensions whose small cohorts must be suppressed
	KAnonExempt map[string]bool // roles exempt from k-anonymity
	CountMetric string          // metric used as the cohort-size measure (e.g. order_count)

	// TenantBudgetBytes caps cumulative estimated scan bytes per tenant over the
	// ledger window (0 = unlimited). Once a tenant's running total exceeds it,
	// further queries are refused — per-tenant spend accounting, not just a
	// per-query ceiling.
	TenantBudgetBytes int64
}

// DefaultPolicy: admin sees raw + is exempt; managers are region-scoped (RLS);
// grouping by customer_email enforces k=5 anonymity.
func DefaultPolicy() Policy {
	return Policy{
		Unmask:      map[string]bool{"admin": true},
		RowFilters:  []RowFilter{{Dimension: "store_region", AttrKey: "region", Roles: []string{"manager"}}},
		K:           5,
		KAnonDims:   []string{"customer_email"},
		KAnonExempt: map[string]bool{"admin": true},
		CountMetric: "order_count",
	}
}

// Query authorizes, applies RLS + k-anonymity, runs, masks, and audits.
func Query(ctx context.Context, eng *engine.Engine, q semantic.Query, p Principal, pol Policy) (*engine.Answer, error) {
	startNs := time.Now().UnixNano()
	// Root OTel span: child compile/plan/execute spans nest under it via ctx, and
	// if a traceparent was propagated in (MCP HTTP boundary) this continues that
	// remote trace. No-op until OTel is initialized.
	ctx, span := obs.Tracer().Start(ctx, "governed_query")
	defer span.End()
	span.SetAttributes(
		attribute.String("role", p.Role),
		attribute.StringSlice("metrics", q.Metrics))
	// 1) Metric RBAC — refuse before any SQL runs.
	if err := authorize(eng.Model, q.Metrics, p.Role); err != nil {
		audit(ctx, eng, p, q, "", true, err.Error())
		return nil, err
	}
	// 1b) Per-tenant spend budget — refuse once the tenant's cumulative cost
	// over the ledger window has exceeded its allowance.
	var ledger *SpendLedger
	if pol.TenantBudgetBytes > 0 {
		ledger = NewSpendLedger(eng.WH)
		if spent, _, lerr := ledger.Spent(ctx, tenantKey(p)); lerr == nil && spent >= pol.TenantBudgetBytes {
			err := fmt.Errorf("tenant %q over budget: %d of %d bytes spent — refused", tenantKey(p), spent, pol.TenantBudgetBytes)
			audit(ctx, eng, p, q, "", true, err.Error())
			return nil, err
		}
	}
	// 2) Row-level security — append filters bound to the caller's attributes.
	if err := applyRLS(&q, p, pol); err != nil {
		audit(ctx, eng, p, q, "", true, err.Error())
		return nil, err
	}
	// 3) k-anonymity prep — add the cohort-count metric when grouping by a protected dim.
	addedCount := false
	if kAnonActive(q, p, pol) {
		q.Metrics = append(q.Metrics, pol.CountMetric)
		addedCount = true
	}
	// 4) Execute under the caller's identity (on-behalf-of): the DB session
	// carries app.user/role/tenant/region so the warehouse's own RLS enforces
	// too — belt-and-suspenders under the app-layer filters added in step 2.
	ans, err := eng.QueryAs(ctx, q, sessionFor(p, pol))
	if err != nil {
		audit(ctx, eng, p, q, "", false, err.Error())
		return nil, err
	}
	// 5) k-anonymity suppress — drop cohorts below K, then hide the count column.
	if addedCount {
		suppressed := kAnonSuppress(ans, pol)
		audit(ctx, eng, p, q, ans.SQL, false, fmt.Sprintf("rows=%d k-anon-suppressed=%d", len(ans.Rows), suppressed))
	}
	// 6) Column masking on the result.
	maskColumns(eng.Model, ans, p.Role, pol)
	if !addedCount {
		audit(ctx, eng, p, q, ans.SQL, false, fmt.Sprintf("rows=%d", len(ans.Rows)))
	}

	// 7) Observability: one trace (compile → execute → rows) with latency.
	tr := obs.New("query", startNs)
	tr.Attrs["role"] = p.Role
	tr.Attrs["metrics"] = q.Metrics
	tr.Attrs["group_by"] = q.GroupBy
	tr.Attrs["rows"] = len(ans.Rows)
	tr.Attrs["est_bytes"] = ans.EstBytes // cost proxy beyond latency
	tr.Add("compile", ans.CompileMs, map[string]any{"sql_len": len(ans.SQL)})
	tr.Add("plan", 0, map[string]any{"est_rows": ans.EstRows, "est_bytes": ans.EstBytes})
	tr.Add("execute", ans.ExecMs, map[string]any{"rows": len(ans.Rows)})
	_ = tr.Finish(ctx, eng.WH, time.Now().UnixNano())
	ans.TraceID = tr.ID

	// Record this query's cost against the tenant's running budget.
	if ledger != nil {
		_ = ledger.Add(ctx, tenantKey(p), ans.EstBytes)
	}

	return ans, nil
}

// sessionFor builds the on-behalf-of DB session. The region GUC is set only for
// roles a region RowFilter applies to (e.g. managers), so the warehouse-level
// RLS scopes exactly the same callers the app-layer filter does — others see all.
func sessionFor(p Principal, pol Policy) warehouse.Session {
	s := warehouse.Session{User: p.User, Role: p.Role, Tenant: p.Attrs["tenant"]}
	for _, rf := range pol.RowFilters {
		if rf.Dimension == "store_region" && roleIn(p.Role, rf.Roles) {
			s.Region = p.Attrs[rf.AttrKey]
		}
	}
	return s
}

func applyRLS(q *semantic.Query, p Principal, pol Policy) error {
	for _, rf := range pol.RowFilters {
		if !roleIn(p.Role, rf.Roles) {
			continue
		}
		val := p.Attrs[rf.AttrKey]
		if val == "" {
			return fmt.Errorf("row policy: caller %q has no %q attribute", p.User, rf.AttrKey)
		}
		q.Where = append(q.Where, semantic.Filter{Dimension: rf.Dimension, Op: "=", Values: []any{val}})
	}
	return nil
}

func kAnonActive(q semantic.Query, p Principal, pol Policy) bool {
	if pol.K <= 0 || pol.CountMetric == "" || pol.KAnonExempt[p.Role] {
		return false
	}
	for _, gb := range q.GroupBy {
		for _, kd := range pol.KAnonDims {
			if gb == kd {
				return true
			}
		}
	}
	return false
}

// kAnonSuppress drops rows whose cohort count < K and removes the count column.
func kAnonSuppress(ans *engine.Answer, pol Policy) int {
	ci := -1
	for i, c := range ans.Columns {
		if c == pol.CountMetric {
			ci = i
		}
	}
	if ci < 0 {
		return 0
	}
	var kept [][]any
	suppressed := 0
	for _, row := range ans.Rows {
		n := toFloat(row[ci])
		if n < float64(pol.K) {
			suppressed++
			continue
		}
		kept = append(kept, append(row[:ci:ci], row[ci+1:]...))
	}
	ans.Rows = kept
	ans.Columns = append(ans.Columns[:ci:ci], ans.Columns[ci+1:]...)
	return suppressed
}

func roleIn(role string, roles []string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case string:
		var f float64
		_, _ = fmt.Sscanf(t, "%g", &f)
		return f
	default:
		var f float64
		_, _ = fmt.Sscanf(fmt.Sprintf("%v", t), "%g", &f)
		return f
	}
}

func authorize(m *semantic.Model, metrics []string, role string) error {
	for _, name := range metrics {
		mt := m.Metric(name)
		if mt == nil {
			continue // unknown metric handled by the compiler
		}
		if len(mt.Roles) == 0 {
			continue
		}
		ok := false
		for _, r := range mt.Roles {
			if r == role {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("metric %q not authorized for role %q", name, role)
		}
	}
	return nil
}

func maskColumns(m *semantic.Model, ans *engine.Answer, role string, pol Policy) {
	if pol.Unmask[role] {
		return
	}
	for ci, col := range ans.Columns {
		d := m.Dimension(col)
		if d == nil || d.Mask == "" {
			continue
		}
		red := stripQuotes(d.Mask)
		for ri := range ans.Rows {
			if ci < len(ans.Rows[ri]) {
				ans.Rows[ri][ci] = red
			}
		}
	}
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

func audit(ctx context.Context, eng *engine.Engine, p Principal, q semantic.Query, sql string, refused bool, note string) {
	_, _ = eng.WH.Exec(ctx, `CREATE TABLE IF NOT EXISTS _audit (
		ts timestamptz DEFAULT now(), "user" text, role text,
		metrics text, group_by text, sql text, refused bool, note text)`)
	_, _ = eng.WH.Exec(ctx,
		`INSERT INTO _audit ("user", role, metrics, group_by, sql, refused, note) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		p.User, p.Role, fmt.Sprintf("%v", q.Metrics), fmt.Sprintf("%v", q.GroupBy), sql, refused, note)
}
