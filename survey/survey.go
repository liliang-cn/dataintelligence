// Package survey is the site survey: what is actually in a customer's database,
// found by looking rather than by asking.
//
// Week one of an engagement is spent discovering that the schema diagram is out
// of date, one feed stopped six months ago, a "status" column has eleven values
// nobody documented, and a third of the foreign keys point at rows that do not
// exist. None of that is visible in a schema dump. All of it changes what can
// be modelled.
//
// The output is meant to be read by a person and argued about with the
// customer. Every figure comes from a query; nothing is inferred from names.
package survey

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Column is one column, as the data actually looks.
type Column struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Nullable bool     `json:"nullable"`
	Nulls    float64  `json:"null_rate"`          // 0..1
	Distinct int64    `json:"distinct,omitempty"` // -1 when not counted
	Sample   []string `json:"sample,omitempty"`   // a few real values, for low-cardinality columns
	Min      string   `json:"min,omitempty"`      // time columns only
	Max      string   `json:"max,omitempty"`
}

// Table is one table, with the findings that matter for modelling it.
type Table struct {
	Name       string   `json:"name"`
	Rows       int64    `json:"rows"`
	PrimaryKey string   `json:"primary_key,omitempty"`
	Columns    []Column `json:"columns"`
	Findings   []string `json:"findings,omitempty"`
}

// Orphan is a declared foreign key whose values do not all exist in the target.
type Orphan struct {
	Table     string `json:"table"`
	Column    string `json:"column"`
	RefTable  string `json:"ref_table"`
	RefColumn string `json:"ref_column"`
	Count     int64  `json:"count"`
}

// Report is the whole survey.
type Report struct {
	Database string    `json:"database"`
	Driver   string    `json:"driver"`
	Tables   []Table   `json:"tables"`
	Orphans  []Orphan  `json:"orphans,omitempty"`
	Findings []string  `json:"findings,omitempty"` // database-level
	TakenAt  time.Time `json:"taken_at"`
	Sampled  int       `json:"sample_rows"` // rows sampled per column profile (0 = full scan)
}

// Options bound the cost. A survey runs against a customer's production
// database on day one, which is the worst possible moment to be the reason it
// got slow.
type Options struct {
	// MaxDistinct caps the distinct-count probe. Columns above it are reported
	// as high-cardinality rather than counted exactly.
	MaxDistinct int64
	// StaleAfter flags a time column whose newest value is older than this.
	// A feed that stopped is the finding most often missed and most expensive.
	StaleAfter time.Duration
	// SkipOrphans skips referential-integrity probes, which are the costliest part.
	SkipOrphans bool
}

func (o *Options) withDefaults() {
	if o.MaxDistinct <= 0 {
		o.MaxDistinct = 50
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 60 * 24 * time.Hour
	}
}

// Run surveys every user table.
func Run(ctx context.Context, wh *warehouse.Warehouse, database string, opts Options) (*Report, error) {
	opts.withDefaults()
	schema, err := modelgen.Introspect(ctx, wh)
	if err != nil {
		return nil, err
	}
	q := quoter(wh.Driver())
	rep := &Report{Database: database, Driver: wh.Driver(), TakenAt: time.Now(), Tables: []Table{}}

	for _, t := range schema.Tables {
		tbl := Table{Name: t.Name, PrimaryKey: t.PrimaryKey}
		tbl.Rows = scalarInt(ctx, wh, fmt.Sprintf("SELECT count(*) FROM %s", q(t.Name)))

		if tbl.Rows == 0 {
			// An empty table in a live system is a question, not a fact: never
			// used, or the feed never arrived?
			tbl.Findings = append(tbl.Findings, "empty — never populated, or the feed never arrived?")
		}
		if t.PrimaryKey == "" {
			tbl.Findings = append(tbl.Findings, "no primary key — rows cannot be counted distinctly, and de-duplication has nothing to key on")
		}

		for _, c := range t.Columns {
			col := Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Distinct: -1}
			if tbl.Rows > 0 {
				col.profile(ctx, wh, q, t.Name, tbl.Rows, opts)
				tbl.Findings = append(tbl.Findings, col.findings(tbl.Rows, opts)...)
			}
			tbl.Columns = append(tbl.Columns, col)
		}
		rep.Tables = append(rep.Tables, tbl)
	}

	if !opts.SkipOrphans {
		rep.Orphans = orphans(ctx, wh, q, schema)
	}
	rep.Findings = summarise(rep)
	return rep, nil
}

// profile fills in what the data looks like, as opposed to what it is declared to be.
func (c *Column) profile(ctx context.Context, wh *warehouse.Warehouse, q func(string) string, table string, rows int64, opts Options) {
	col, tbl := q(c.Name), q(table)

	nulls := scalarInt(ctx, wh, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NULL", tbl, col))
	if rows > 0 {
		c.Nulls = math.Round(float64(nulls)/float64(rows)*1000) / 1000
	}

	// Cap the distinct probe: on a wide fact table an exact count of a
	// high-cardinality column is a full scan for a number nobody needs.
	// NULL is excluded: counting it as a value makes an all-null column look
	// like a constant, and the report then says both "entirely null" and "one
	// value in every row" about the same column — two findings, one of them
	// wrong, which is how a reader stops trusting the rest.
	d := scalarInt(ctx, wh, fmt.Sprintf(
		"SELECT count(*) FROM (SELECT DISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT %d) _d",
		col, tbl, col, opts.MaxDistinct+1))
	if d > opts.MaxDistinct {
		c.Distinct = -1 // high cardinality: an identifier or a measure, not a dimension
	} else {
		c.Distinct = d
		// A column with few values is a dimension in waiting. The actual values
		// are what a customer recognises — "status has eleven values and three
		// of them are typos" is a conversation the survey should start.
		if d > 0 && d <= opts.MaxDistinct && !isNumericType(c.Type) {
			c.Sample = queryStrings(ctx, wh, fmt.Sprintf(
				"SELECT DISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT 12", col, tbl, col))
		}
	}

	if isTimeType(c.Type) {
		cast := textCast(wh.Driver())
		if r := queryStrings(ctx, wh, fmt.Sprintf("SELECT %s, %s FROM %s",
			cast("MIN("+col+")"), cast("MAX("+col+")"), tbl)); len(r) == 2 {
			c.Min, c.Max = r[0], r[1]
		}
	}
}

// MinRowsForCadence is exported so the Day 2 check uses the same judgement as
// the survey. Two copies of "is this feed stale" would drift apart, and the one
// that runs on a schedule is the one whose false alarms teach people to ignore
// it.
const MinRowsForCadence = minRowsForCadence

// minRowsForCadence is the point below which "nothing new in a while" stops
// meaning anything. Ten stores that last opened in 2022 is a chain that stopped
// expanding, not a broken feed — and a survey that says otherwise teaches the
// reader to skim the section it most needs them to read.
const minRowsForCadence = 30

func (c *Column) findings(rows int64, opts Options) []string {
	var out []string
	switch {
	case c.Nulls == 1:
		out = append(out, fmt.Sprintf("%s is entirely null — nothing can be modelled on it", c.Name))
	case c.Nulls >= 0.5:
		out = append(out, fmt.Sprintf("%s is %.0f%% null — check whether it is optional or broken", c.Name, c.Nulls*100))
	}
	if c.Distinct == 1 && rows > 1 {
		out = append(out, fmt.Sprintf("%s has one value in every row — a constant, not a dimension", c.Name))
	}
	// The finding most often missed, and the most expensive: a source that
	// stopped sending. Every row is valid; the totals quietly stop growing.
	if c.Max != "" && rows >= minRowsForCadence {
		if newest, ok := parseTime(c.Max); ok && time.Since(newest) > opts.StaleAfter {
			out = append(out, fmt.Sprintf("%s stops at %s — has this feed stopped?", c.Name, c.Max))
		}
	}
	return out
}

func orphans(ctx context.Context, wh *warehouse.Warehouse, q func(string) string, schema *modelgen.Schema) []Orphan {
	var out []Orphan
	for _, t := range schema.Tables {
		for _, fk := range t.ForeignKeys {
			n := scalarInt(ctx, wh, fmt.Sprintf(
				"SELECT count(*) FROM %s c WHERE c.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s p WHERE p.%s = c.%s)",
				q(t.Name), q(fk.Column), q(fk.RefTable), q(fk.RefColumn), q(fk.Column)))
			if n > 0 {
				out = append(out, Orphan{Table: t.Name, Column: fk.Column,
					RefTable: fk.RefTable, RefColumn: fk.RefColumn, Count: n})
			}
		}
	}
	return out
}

// summarise raises the database-level observations: the ones that decide
// whether this warehouse can be modelled at all.
func summarise(r *Report) []string {
	var out []string
	var empty, keyless int
	var total int64
	for _, t := range r.Tables {
		total += t.Rows
		if t.Rows == 0 {
			empty++
		}
		if t.PrimaryKey == "" {
			keyless++
		}
	}
	if len(r.Tables) == 0 {
		return []string{"no user tables — is this the right database, or the right schema?"}
	}
	out = append(out, fmt.Sprintf("%d tables, %s rows in total", len(r.Tables), humanInt(total)))
	if empty > 0 {
		out = append(out, fmt.Sprintf("%d table(s) are empty", empty))
	}
	if keyless > 0 {
		out = append(out, fmt.Sprintf("%d table(s) have no primary key — de-duplication and distinct counts have nothing to key on", keyless))
	}
	// Several tables ending on the same day is one event, not several findings.
	// Reported per-table it reads as four unrelated problems; reported once it
	// reads as what it is — a load that stopped, or a cutover nobody mentioned.
	if date, tables := sharedCutoff(r); len(tables) > 1 {
		out = append(out, fmt.Sprintf(
			"%d tables all stop at %s: %s — one event, not %d problems. A load that stopped, or a migration nobody mentioned?",
			len(tables), date, strings.Join(tables, ", "), len(tables)))
	}
	if n := len(r.Orphans); n > 0 {
		var rows int64
		for _, o := range r.Orphans {
			rows += o.Count
		}
		// Broken referential integrity decides the join graph. A join the
		// database does not actually honour silently drops rows.
		out = append(out, fmt.Sprintf("%d foreign key(s) are not honoured by the data — %s orphan row(s); joins on them will drop rows", n, humanInt(rows)))
	}
	return out
}

// sharedCutoff finds a date several tables stop at.
func sharedCutoff(r *Report) (string, []string) {
	byDate := map[string][]string{}
	for _, t := range r.Tables {
		if t.Rows < minRowsForCadence {
			continue
		}
		newest := ""
		for _, c := range t.Columns {
			if c.Max > newest {
				newest = c.Max
			}
		}
		if day := dayOf(newest); day != "" {
			byDate[day] = append(byDate[day], t.Name)
		}
	}
	bestDate, best := "", []string(nil)
	for day, tables := range byDate {
		if len(tables) > len(best) {
			bestDate, best = day, tables
		}
	}
	sort.Strings(best)
	return bestDate, best
}

func dayOf(s string) string {
	if t, ok := parseTime(s); ok {
		return t.Format("2006-01-02")
	}
	return ""
}

// ── engine differences ───────────────────────────────────────────────────────

func quoter(driver string) func(string) string {
	switch driver {
	case "mysql":
		return func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	case "sqlserver":
		return func(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }
	default:
		return func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	}
}

// textCast renders a value as unpadded text. Postgres' CHAR(n) is blank-padded,
// which puts a trail of spaces into every timestamp in the report.
func textCast(driver string) func(string) string {
	switch driver {
	case "mysql":
		return func(e string) string { return "CAST(" + e + " AS CHAR)" }
	case "sqlserver":
		return func(e string) string { return "CONVERT(varchar(40), " + e + ", 126)" }
	case "duckdb":
		return func(e string) string { return "CAST(" + e + " AS VARCHAR)" }
	default: // postgres, sqlite
		return func(e string) string { return "CAST(" + e + " AS TEXT)" }
	}
}

func isTimeType(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "date") || strings.Contains(t, "time") || strings.Contains(t, "stamp")
}

func isNumericType(t string) bool {
	t = strings.ToLower(t)
	for _, n := range []string{"int", "numeric", "decimal", "float", "double", "real", "money"} {
		if strings.Contains(t, n) {
			return true
		}
	}
	return false
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ── small query helpers ──────────────────────────────────────────────────────
//
// A survey is best-effort by design: one column that cannot be profiled must
// not abort the report. A partial survey is useful; a failed one is not.

func scalarInt(ctx context.Context, wh *warehouse.Warehouse, q string) int64 {
	res, err := wh.Query(ctx, q)
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	switch v := res.Rows[0][0].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		var n int64
		_, _ = fmt.Sscan(string(v), &n)
		return n
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	default:
		return 0
	}
}

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func queryStrings(ctx context.Context, wh *warehouse.Warehouse, q string) []string {
	res, err := wh.Query(ctx, q)
	if err != nil {
		return nil
	}
	var out []string
	for _, row := range res.Rows {
		for _, cell := range row {
			out = append(out, strings.TrimSpace(cellString(cell)))
		}
	}
	sort.Strings(out)
	return out
}

func humanInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
