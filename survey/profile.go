package survey

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// profileTable fills in every column of one table in as few scans as possible.
//
// The naive shape — four probes per column — cost 527 queries on thirteen small
// tables, and every one of them is a scan. On a real warehouse, where a fact
// table has two hundred columns and five hundred million rows, that is eight
// hundred full scans issued against production on day one, by the engineer who
// is trying to earn the customer's trust. The package comment says a survey
// must not be the reason the database got slow; it was bounding cardinality,
// which is the wrong quantity.
//
// So the null counts and the ranges come from a single pass over the table —
// count(col) is the non-null count, and MIN/MAX ride along in the same scan.
// Only the distinct counts still need a probe each, and those are the ones that
// can be sampled.
func profileTable(ctx context.Context, wh *warehouse.Warehouse, q func(string) string,
	t *Table, opts Options) {
	if t.Rows == 0 {
		return
	}
	aggregatePass(ctx, wh, q, t, opts)

	sample := samplerFor(wh.Driver(), t.Rows, opts)
	for i := range t.Columns {
		distinctProbe(ctx, wh, q, t.Name, &t.Columns[i], sample, opts)
	}
}

// aggregatePass gets the null rate of every column and the range of every time
// column in one query.
//
// Chunked, because an engine will not take a thousand expressions in one
// select, and because a statement that long is unreadable in a slow-query log —
// which is where the customer's DBA will meet it.
func aggregatePass(ctx context.Context, wh *warehouse.Warehouse, q func(string) string,
	t *Table, opts Options) {
	const maxExprs = 60
	cast := textCast(wh.Driver())

	type slot struct {
		col   int
		kind  string // notnull | min | max
		index int
	}
	var exprs []string
	var slots []slot
	flush := func() {
		if len(exprs) == 0 {
			return
		}
		row := queryRow(ctx, wh, fmt.Sprintf("SELECT %s FROM %s", strings.Join(exprs, ", "), q(t.Name)))
		for _, s := range slots {
			if s.index >= len(row) {
				continue
			}
			c := &t.Columns[s.col]
			switch s.kind {
			case "notnull":
				var nn int64
				_, _ = fmt.Sscan(strings.TrimSpace(row[s.index]), &nn)
				c.Nulls = math.Round(float64(t.Rows-nn)/float64(t.Rows)*1000) / 1000
			case "min":
				c.Min = strings.TrimSpace(row[s.index])
			case "max":
				c.Max = strings.TrimSpace(row[s.index])
			}
		}
		exprs, slots = nil, nil
	}

	for i := range t.Columns {
		c := &t.Columns[i]
		col := q(c.Name)
		if len(exprs)+3 > maxExprs {
			flush()
		}
		slots = append(slots, slot{col: i, kind: "notnull", index: len(exprs)})
		exprs = append(exprs, "count("+col+")")
		// Ranges are never sampled, and only ever come from this exact pass.
		// The stale-feed finding is the most valuable thing the survey produces
		// and it turns entirely on the true MAX: a sampled maximum invents a
		// stopped feed, or hides one, and both are worse than not checking.
		if isTimeType(c.Type) {
			slots = append(slots,
				slot{col: i, kind: "min", index: len(exprs)},
				slot{col: i, kind: "max", index: len(exprs) + 1})
			exprs = append(exprs, cast("MIN("+col+")"), cast("MAX("+col+")"))
		}
	}
	flush()
}

// sampler describes how to read a bounded slice of a table.
type sampler struct {
	clause string  // goes after the table name, or ""
	pct    float64 // what fraction of the table it reads, for the report
	biased bool    // true when it is the first N rows rather than a random draw
}

func (s sampler) on() bool { return s.clause != "" }

// samplerFor picks a sampling clause, or none.
//
// Below the threshold nothing is sampled, and the threshold is high on purpose.
// An exact distinct count is worth much more than an estimate — "status has
// eleven values and three are typos" is a conversation, "status has about
// eleven values" is not — so sampling only starts where an exact count stops
// being affordable.
func samplerFor(driver string, rows int64, opts Options) sampler {
	if opts.SampleAbove <= 0 || rows <= opts.SampleAbove {
		return sampler{}
	}
	n := opts.SampleRows
	if n <= 0 {
		n = defaultSampleRows
	}
	if n >= rows {
		return sampler{}
	}
	pct := float64(n) / float64(rows)
	switch driver {
	case "pgx", "postgres":
		// SYSTEM reads whole blocks and skips the rest, so it is genuinely
		// cheap. BERNOULLI would still touch every row, which is the thing
		// being avoided.
		return sampler{clause: fmt.Sprintf(" TABLESAMPLE SYSTEM (%.4f)", math.Max(pct*100, 0.0001)), pct: pct}
	case "sqlserver":
		return sampler{clause: fmt.Sprintf(" TABLESAMPLE (%d ROWS)", n), pct: pct}
	case "duckdb":
		return sampler{clause: fmt.Sprintf(" USING SAMPLE %d ROWS", n), pct: pct}
	default:
		// MySQL and SQLite have no sampling clause. A LIMIT bounds the work but
		// takes the rows in storage order, which is usually insertion order —
		// so it is the oldest rows, not a cross-section. That is still worth
		// doing, and it is worth saying, because "no value seen after 2019"
		// would otherwise read as a finding rather than as an artefact.
		return sampler{clause: fmt.Sprintf(" LIMIT %d", n), pct: pct, biased: true}
	}
}

const defaultSampleRows = 200_000

// distinctProbe counts a column's distinct values, capped and optionally sampled.
func distinctProbe(ctx context.Context, wh *warehouse.Warehouse, q func(string) string,
	table string, c *Column, s sampler, opts Options) {
	col, tbl := q(c.Name), q(table)

	// A LIMIT-based sampler has to wrap the table; a TABLESAMPLE clause attaches
	// to it. Both end up as a relation the distinct count reads from.
	from := tbl + s.clause
	if s.on() && s.biased {
		from = fmt.Sprintf("(SELECT %s FROM %s%s) _s", col, tbl, s.clause)
	}

	// NULL is excluded: counting it as a value makes an all-null column look
	// like a constant, and the report then says both "entirely null" and "one
	// value in every row" about the same column — two findings, one of them
	// wrong, which is how a reader stops trusting the rest.
	count := func(from string) (int64, bool) {
		return scalarIntOK(ctx, wh, fmt.Sprintf(
			"SELECT count(*) FROM (SELECT DISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT %d) _d",
			col, from, col, opts.MaxDistinct+1))
	}

	d, ok := count(from)
	if !ok && s.on() {
		// TABLESAMPLE does not apply to views, and some engines refuse it on
		// partitioned or foreign tables. Falling back is right; failing to
		// notice and reporting zero distinct values is not — that column would
		// be described as empty in a document going to the customer.
		d, ok, s = 0, false, sampler{}
		d, ok = count(tbl)
	}
	if !ok {
		c.Distinct = -1
		return
	}
	c.Approx = s.on()
	c.SampledPct = s.pct
	c.Biased = s.biased

	if d > opts.MaxDistinct {
		c.Distinct = -1 // high cardinality: an identifier or a measure, not a dimension
		return
	}
	c.Distinct = d
	// The actual values are what a customer recognises — "status has eleven
	// values and three of them are typos" is a conversation the survey should
	// start.
	if d > 0 && !isNumericType(c.Type) {
		c.Sample = queryStrings(ctx, wh, fmt.Sprintf(
			"SELECT DISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT 12", col, from, col))
	}
}

// queryRow returns the single row of a query as strings.
func queryRow(ctx context.Context, wh *warehouse.Warehouse, sql string) []string {
	res, err := wh.Query(ctx, sql)
	if err != nil || len(res.Rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(res.Rows[0]))
	for _, cell := range res.Rows[0] {
		out = append(out, cellString(cell))
	}
	return out
}

// scalarIntOK is scalarInt that admits failure.
//
// The silent version returns zero on error, and zero is a legitimate answer
// here — so a probe that failed became "this column has no distinct values",
// which reads as "this column is empty" in a document going to a customer.
func scalarIntOK(ctx context.Context, wh *warehouse.Warehouse, sql string) (int64, bool) {
	res, err := wh.Query(ctx, sql)
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, false
	}
	var n int64
	if _, err := fmt.Sscan(strings.TrimSpace(cellString(res.Rows[0][0])), &n); err != nil {
		return 0, false
	}
	return n, true
}
