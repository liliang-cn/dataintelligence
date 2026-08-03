// Package modelgen turns a live warehouse into a semantic-model draft: introspect
// the schema (tables, columns, keys, foreign keys), then generate entities /
// joins / dimensions / metrics — heuristically, optionally refined by an LLM.
// It is domain-neutral: it reads whatever schema it is pointed at and never
// assumes a particular business. The output is a starting point a human reviews,
// not a finished model.
package modelgen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Column is one table column.
type Column struct {
	Name     string
	Type     string // information_schema data_type
	Nullable bool
	// Categorical marks an integer column the data says is a code: few distinct
	// values, so summing it means nothing.
	Categorical bool
}

// ForeignKey is a declared FK edge (this table's Column → RefTable.RefColumn).
type ForeignKey struct {
	Column    string
	RefTable  string
	RefColumn string
}

// Table is one introspected relation.
type Table struct {
	Name        string
	Columns     []Column
	PrimaryKey  string
	ForeignKeys []ForeignKey
}

// Schema is the introspected warehouse (user tables only).
type Schema struct {
	Tables []Table
}

// probe is the set of engine-specific catalogue queries introspection needs.
// The catalogue is the one place a "portable" SQL layer cannot stay portable:
// Postgres scopes by schema and joins constraint_column_usage to reach a foreign
// key's target, while MySQL scopes by database and carries the target on
// key_column_usage itself — and has no constraint_column_usage at all.
type probe struct {
	tables, columns, primaryKey, foreignKeys string
}

var probes = map[string]probe{
	"pgx": {
		tables: `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`,
		columns: `SELECT column_name, data_type, is_nullable
			FROM information_schema.columns WHERE table_schema='public' AND table_name=$1
			ORDER BY ordinal_position`,
		primaryKey: `SELECT kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
			WHERE tc.constraint_type='PRIMARY KEY' AND tc.table_schema='public' AND tc.table_name=$1
			ORDER BY kcu.ordinal_position LIMIT 1`,
		foreignKeys: `SELECT kcu.column_name, ccu.table_name, ccu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
			JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name=tc.constraint_name
			WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_schema='public' AND tc.table_name=$1`,
	},
	"mysql": {
		// BASE TABLE excludes views, which have no keys to model from.
		tables: `SELECT table_name FROM information_schema.tables
			WHERE table_schema=DATABASE() AND table_type='BASE TABLE' ORDER BY table_name`,
		columns: `SELECT column_name, data_type, is_nullable
			FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?
			ORDER BY ordinal_position`,
		// MySQL names every primary key "PRIMARY"; there is no constraint_type here.
		primaryKey: `SELECT column_name FROM information_schema.key_column_usage
			WHERE table_schema=DATABASE() AND table_name=? AND constraint_name='PRIMARY'
			ORDER BY ordinal_position LIMIT 1`,
		foreignKeys: `SELECT column_name, referenced_table_name, referenced_column_name
			FROM information_schema.key_column_usage
			WHERE table_schema=DATABASE() AND table_name=? AND referenced_table_name IS NOT NULL`,
	},
	"sqlite": {
		// SQLite has no information_schema at all; the catalogue is sqlite_master
		// plus the pragma_* table-valued functions (SQLite 3.16+), which — unlike
		// bare PRAGMA — accept a bind parameter.
		tables: `SELECT name FROM sqlite_master
			WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
		// notnull is 0/1 here; map it to the YES/NO the caller expects.
		columns: `SELECT name, type, CASE "notnull" WHEN 0 THEN 'YES' ELSE 'NO' END
			FROM pragma_table_info(?)`,
		primaryKey:  `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk LIMIT 1`,
		foreignKeys: `SELECT "from", "table", "to" FROM pragma_foreign_key_list(?)`,
	},
	"sqlserver": {
		tables: `SELECT table_name FROM information_schema.tables
			WHERE table_type='BASE TABLE' AND table_schema='dbo' ORDER BY table_name`,
		columns: `SELECT column_name, data_type, is_nullable
			FROM information_schema.columns WHERE table_schema='dbo' AND table_name=@p1
			ORDER BY ordinal_position`,
		primaryKey: `SELECT TOP 1 kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
			WHERE tc.constraint_type='PRIMARY KEY' AND tc.table_schema='dbo' AND tc.table_name=@p1
			ORDER BY kcu.ordinal_position`,
		foreignKeys: `SELECT kcu.column_name, ccu.table_name, ccu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
			JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name=tc.constraint_name
			WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_schema='dbo' AND tc.table_name=@p1`,
	},
}

// TableNames lists the user tables of a warehouse on any supported engine.
//
// It exists because "list the tables" was written three times — once per
// surface — each time against pg_tables, and each copy broke silently the day
// the warehouse was not Postgres.
func TableNames(ctx context.Context, wh *warehouse.Warehouse) ([]string, error) {
	p, ok := probes[wh.Driver()]
	if !ok {
		return nil, fmt.Errorf("no schema introspection for driver %q", wh.Driver())
	}
	res, err := wh.Query(ctx, p.tables)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range res.Rows {
		if name := str(row[0]); name != "" && !strings.HasPrefix(name, "_") {
			out = append(out, name)
		}
	}
	return out, nil
}

// Introspect reads the user tables of a warehouse, skipping the platform's own
// bookkeeping tables (those prefixed with "_").
func Introspect(ctx context.Context, wh *warehouse.Warehouse) (*Schema, error) {
	p, ok := probes[wh.Driver()]
	if !ok {
		return nil, fmt.Errorf("modelgen: no schema introspection for driver %q — supported: postgres, mysql, sqlite, sqlserver", wh.Driver())
	}
	res, err := wh.Query(ctx, p.tables)
	if err != nil {
		return nil, err
	}
	var s Schema
	for _, row := range res.Rows {
		name := str(row[0])
		if name == "" || strings.HasPrefix(name, "_") {
			continue // skip _flow_runs / _traces / _spend / _model_versions / _audit / _nl_eval_*
		}
		t := Table{Name: name}
		if t.Columns, err = columnsOf(ctx, wh, p, name); err != nil {
			return nil, err
		}
		if t.PrimaryKey, err = primaryKeyOf(ctx, wh, p, name); err != nil {
			return nil, err
		}
		if t.ForeignKeys, err = foreignKeysOf(ctx, wh, p, name); err != nil {
			return nil, err
		}
		if wh.Driver() == "sqlite" {
			retypeSQLiteDates(ctx, wh, &t)
		}
		markCategoricalIntegers(ctx, wh, &t, rowsOf(ctx, wh, t.Name))
		s.Tables = append(s.Tables, t)
	}
	return &s, nil
}

// retypeSQLiteDates promotes TEXT columns that actually hold dates.
//
// SQLite has no date type: a timestamp column is declared TEXT and is
// indistinguishable, in the catalogue, from a product name. Left alone the
// generated model calls it categorical, and then a "revenue by month" request
// silently returns daily rows — right arithmetic, wrong question. The compiler
// now refuses that combination, which turns the silence into an error; this
// turns the error into a correct model.
//
// The test is evidence, not the column name: sample a few real values and see
// whether they parse. A column called order_date holding "Q3" stays text.
func retypeSQLiteDates(ctx context.Context, wh *warehouse.Warehouse, t *Table) {
	for i, c := range t.Columns {
		if !strings.EqualFold(c.Type, "text") {
			continue
		}
		q := fmt.Sprintf(`SELECT "%s" FROM "%s" WHERE "%s" IS NOT NULL LIMIT 5`,
			strings.ReplaceAll(c.Name, `"`, `""`), strings.ReplaceAll(t.Name, `"`, `""`),
			strings.ReplaceAll(c.Name, `"`, `""`))
		res, err := wh.Query(ctx, q)
		if err != nil || len(res.Rows) == 0 {
			continue
		}
		all := true
		for _, row := range res.Rows {
			if !looksLikeTimestamp(str(row[0])) {
				all = false
				break
			}
		}
		if all {
			t.Columns[i].Type = "timestamp"
		}
	}
}

// looksLikeTimestamp accepts the formats SQLite's own date functions understand.
// Anything else is left as text rather than guessed at.
func looksLikeTimestamp(v string) bool {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02",
	} {
		if _, err := time.Parse(layout, v); err == nil {
			return true
		}
	}
	return false
}

func columnsOf(ctx context.Context, wh *warehouse.Warehouse, p probe, table string) ([]Column, error) {
	res, err := wh.Query(ctx, p.columns, table)
	if err != nil {
		return nil, err
	}
	var cols []Column
	for _, row := range res.Rows {
		cols = append(cols, Column{Name: str(row[0]), Type: str(row[1]), Nullable: str(row[2]) == "YES"})
	}
	return cols, nil
}

func primaryKeyOf(ctx context.Context, wh *warehouse.Warehouse, p probe, table string) (string, error) {
	res, err := wh.Query(ctx, p.primaryKey, table)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", nil
	}
	return str(res.Rows[0][0]), nil
}

func foreignKeysOf(ctx context.Context, wh *warehouse.Warehouse, p probe, table string) ([]ForeignKey, error) {
	res, err := wh.Query(ctx, p.foreignKeys, table)
	if err != nil {
		return nil, err
	}
	var fks []ForeignKey
	for _, row := range res.Rows {
		fks = append(fks, ForeignKey{Column: str(row[0]), RefTable: str(row[1]), RefColumn: str(row[2])})
	}
	return fks, nil
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// categoricalCeiling is how many distinct values an integer column may hold and
// still be a category rather than a measure.
const categoricalCeiling = 24

// markCategoricalIntegers finds integer columns that name something instead of
// measuring it.
//
// Names catch most of them — order_id, sku — but not all: SAP's client column
// is MANDT, and the draft proposed "sum of MANDT", the sum of a client number.
//
// Where the name is silent, the *shape* of the values is not. A quantity is a
// dense run from zero or one: seven distinct values 1..7 is a quantity. A code
// is drawn from an arbitrary set: two distinct values 100 and 200 span a
// hundred and one integers, so it is a client number.
//
// The rule stays deliberately narrow. Dropping a real measure is worse than
// leaving a silly one in, because a missing metric is invisible and a silly one
// is not — so anything that could be a quantity keeps its metric, and only the
// unmistakable codes are demoted. The probe is bounded by a LIMIT and costs the
// same on a large fact table as on a small one; an unbounded count(DISTINCT)
// over a warehouse is how this tool gets banned from production on day one.
func markCategoricalIntegers(ctx context.Context, wh *warehouse.Warehouse, t *Table, rows int64) {
	var ints []int
	for i := range t.Columns {
		if isIntegerType(t.Columns[i].Type) {
			ints = append(ints, i)
		}
	}
	if len(ints) == 0 {
		return
	}
	// Above this the probe is not worth its scan, and the safe answer is to
	// leave the column as a measure: dropping a real measure is worse than
	// leaving a silly one in, because a missing metric is invisible.
	if rows > maxRowsForCategoricalProbe {
		return
	}

	// One query for every integer column in the table, not one per column. The
	// per-column form was a scan each — thirty scans of a fact table to decide
	// whether thirty columns are codes, issued against production the first
	// time anyone runs `di model gen`.
	d := wh.Dialect()
	exprs := make([]string, 0, len(ints)*3)
	for _, i := range ints {
		col := d.QuoteIdent(t.Columns[i].Name)
		exprs = append(exprs, "count(DISTINCT "+col+")", "min("+col+")", "max("+col+")")
	}
	res, err := wh.Query(ctx, fmt.Sprintf("SELECT %s FROM %s",
		strings.Join(exprs, ", "), d.QuoteIdent(t.Name)))
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) < len(exprs) {
		return // a probe that fails must not change the draft
	}
	row := res.Rows[0]
	for k, i := range ints {
		n, ok1 := asInt(row[k*3])
		lo, ok2 := asInt(row[k*3+1])
		hi, ok3 := asInt(row[k*3+2])
		if ok1 && ok2 && ok3 {
			t.Columns[i].Categorical = isCodeNotQuantity(n, lo, hi)
		}
	}
}

// maxRowsForCategoricalProbe bounds where the one-pass probe is affordable.
const maxRowsForCategoricalProbe = 20_000_000

// isCodeNotQuantity applies the rule: few distinct values that do not form a
// dense run from zero or one.
func isCodeNotQuantity(distinct, lo, hi int64) bool {
	if distinct <= 1 || distinct > categoricalCeiling {
		return false
	}
	return !(lo <= 1 && hi-lo+1 == distinct)
}

func isIntegerType(t string) bool {
	t = strings.ToLower(t)
	for _, k := range []string{"int", "serial", "number"} {
		if strings.Contains(t, k) {
			return !strings.Contains(t, "point") && !strings.Contains(t, "interval")
		}
	}
	return false
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func rowsOf(ctx context.Context, wh *warehouse.Warehouse, table string) int64 {
	res, err := wh.Query(ctx, "SELECT count(*) FROM "+wh.Dialect().QuoteIdent(table))
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	n, _ := asInt(res.Rows[0][0])
	return n
}
