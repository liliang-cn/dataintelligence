package governance

import (
	"context"
	"fmt"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// SpendLedger accumulates query cost per tenant over time, so a budget can be
// enforced across many queries — not just the per-query byte ceiling. Cost is
// the planner's estimated scan bytes (the same signal the trace records), summed
// per tenant in an append-updated row.
type SpendLedger struct {
	wh *warehouse.Warehouse
}

func NewSpendLedger(wh *warehouse.Warehouse) *SpendLedger { return &SpendLedger{wh: wh} }

func (l *SpendLedger) ensure(ctx context.Context) error {
	_, err := l.wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _spend (
		tenant text PRIMARY KEY, bytes bigint NOT NULL DEFAULT 0,
		queries bigint NOT NULL DEFAULT 0, updated_at timestamptz DEFAULT now())`)
	return err
}

// Spent returns a tenant's cumulative bytes and query count.
func (l *SpendLedger) Spent(ctx context.Context, tenant string) (bytes int64, queries int64, err error) {
	if err = l.ensure(ctx); err != nil {
		return 0, 0, err
	}
	res, err := l.wh.Query(ctx, `SELECT bytes, queries FROM _spend WHERE tenant=$1`, tenant)
	if err != nil {
		return 0, 0, err
	}
	if len(res.Rows) == 0 {
		return 0, 0, nil
	}
	bytes = toInt64(res.Rows[0][0])
	queries = toInt64(res.Rows[0][1])
	return bytes, queries, nil
}

// Add records the cost of one query against a tenant.
func (l *SpendLedger) Add(ctx context.Context, tenant string, bytes int64) error {
	if err := l.ensure(ctx); err != nil {
		return err
	}
	_, err := l.wh.Exec(ctx, `INSERT INTO _spend (tenant, bytes, queries, updated_at)
		VALUES ($1,$2,1, now())
		ON CONFLICT (tenant) DO UPDATE SET bytes=_spend.bytes+$2, queries=_spend.queries+1, updated_at=now()`,
		tenant, bytes)
	return err
}

// Reset zeroes a tenant's ledger (e.g. at the start of a new billing window).
func (l *SpendLedger) Reset(ctx context.Context, tenant string) error {
	if err := l.ensure(ctx); err != nil {
		return err
	}
	_, err := l.wh.Exec(ctx, `DELETE FROM _spend WHERE tenant=$1`, tenant)
	return err
}

// TenantSpend is one ledger row (for the dashboard / CLI).
type TenantSpend struct {
	Tenant  string `json:"tenant"`
	Bytes   int64  `json:"bytes"`
	Queries int64  `json:"queries"`
}

// All lists the ledger, highest spend first.
func (l *SpendLedger) All(ctx context.Context) ([]TenantSpend, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	res, err := l.wh.Query(ctx, `SELECT tenant, bytes, queries FROM _spend ORDER BY bytes DESC`)
	if err != nil {
		return nil, err
	}
	out := make([]TenantSpend, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, TenantSpend{Tenant: fmt.Sprint(row[0]), Bytes: toInt64(row[1]), Queries: toInt64(row[2])})
	}
	return out, nil
}

// tenantKey derives the spend tenant for a principal: an explicit tenant
// attribute, else the user.
func tenantKey(p Principal) string {
	if t := p.Attrs["tenant"]; t != "" {
		return t
	}
	return p.User
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		var x int64
		_, _ = fmt.Sscan(fmt.Sprint(v), &x)
		return x
	}
}
