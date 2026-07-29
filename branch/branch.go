// Package branch is the pre-ingestion gate: load a batch into a copy of the
// affected tables, compare the aggregates against production, and only then
// decide whether to keep it.
//
// Row-level checks — types, primary keys, foreign keys — do not catch the
// expensive failures, because those are aggregate-level. A file imported twice.
// An amount column that switched from yuan to cents. A store that quietly
// stopped reporting. Every row is valid; the totals are wrong; nothing errors.
//
// A branch is a `br_<name>` schema in the same database, filled with
// `CREATE TABLE … AS TABLE`. Postgres' own `CREATE DATABASE … TEMPLATE` needs
// the template to have no live connections, which no production warehouse can
// offer; a schema copy is instant on a live database.
//
// The safety invariant: writes only ever touch `br_*`. Promoting back into
// public is a separate, explicitly requested step — and it is one transaction,
// so a failure halfway cannot leave production with some tables replaced, some
// not, and one of them empty.
package branch

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// requirePostgres states the limit rather than half-implementing it. In MySQL a
// schema *is* a database and in SQLite the database is one file, so the copy,
// the diff and the promote each need a different mechanism there. Saying so is
// better than producing something that looks right on one engine.
func requirePostgres(wh *warehouse.Warehouse) error {
	if wh.Driver() != "pgx" {
		return fmt.Errorf("pre-ingestion branching currently supports PostgreSQL only " +
			"(in MySQL a schema is a database, and SQLite is a single file — each needs its own mechanism)")
	}
	return nil
}

// safeName keeps a branch name out of the SQL it is concatenated into.
func safeName(name string) (string, error) {
	if name == "" || len(name) > 40 {
		return "", fmt.Errorf("branch name must be 1-40 characters")
	}
	for _, c := range name {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", fmt.Errorf("branch name may only contain letters, digits and underscore")
		}
	}
	return name, nil
}

func schemaOf(name string) string { return "br_" + name }

func quote(id string) string { return `"` + strings.ReplaceAll(id, `"`, `""`) + `"` }

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Create copies the chosen tables (all of them, if none are named) into the
// branch schema, replacing any previous branch of that name.
func Create(ctx context.Context, wh *warehouse.Warehouse, name string, tables []string) (map[string]any, error) {
	name, err := safeName(name)
	if err != nil {
		return nil, err
	}
	if err := requirePostgres(wh); err != nil {
		return nil, err
	}
	schema := schemaOf(name)

	all, err := publicTables(ctx, wh)
	if err != nil {
		return nil, err
	}
	picked := all
	if len(tables) > 0 {
		picked = nil
		for _, t := range tables {
			for _, a := range all {
				if a == t {
					picked = append(picked, t)
					break
				}
			}
		}
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("no tables to copy")
	}
	if _, err := wh.Exec(ctx, "DROP SCHEMA IF EXISTS "+quote(schema)+" CASCADE"); err != nil {
		return nil, err
	}
	if _, err := wh.Exec(ctx, "CREATE SCHEMA "+quote(schema)); err != nil {
		return nil, err
	}
	for _, t := range picked {
		if _, err := wh.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.%s AS TABLE public.%s",
			quote(schema), quote(t), quote(t))); err != nil {
			return nil, err
		}
	}
	return map[string]any{"ok": true, "branch": name, "schema": schema, "tables": picked}, nil
}

func publicTables(ctx context.Context, wh *warehouse.Warehouse) ([]string, error) {
	res, err := wh.Query(ctx, `SELECT tablename FROM pg_tables
		WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range res.Rows {
		if s := str(row[0]); s != "" && !strings.HasPrefix(s, "_") {
			out = append(out, s)
		}
	}
	return out, nil
}

// SumDiff compares one numeric column's total.
type SumDiff struct {
	Main   float64 `json:"main"`
	Branch float64 `json:"branch"`
	Delta  float64 `json:"delta"`
	Pct    float64 `json:"pct"`
}

// TableDiff is one table's before/after.
type TableDiff struct {
	Table      string             `json:"table"`
	RowsMain   int64              `json:"rows_main"`
	RowsBranch int64              `json:"rows_branch"`
	RowsDelta  int64              `json:"rows_delta"`
	RowsPct    float64            `json:"rows_pct"`
	Sums       map[string]SumDiff `json:"sums"`
	Flags      []string           `json:"flags"`
}

// Report is the whole comparison, plus the signals worth a human's attention.
type Report struct {
	Branch    string      `json:"branch"`
	Schema    string      `json:"schema"`
	Tables    []TableDiff `json:"tables"`
	FlagCount int         `json:"flag_count"`
	Verdict   string      `json:"verdict"`
}

// Pct is the percentage change from main to branch, rounded to 2 decimals.
// A zero baseline cannot yield a ratio: growing from nothing is reported as
// 100% rather than as infinity or a silent zero.
func Pct(main, branch float64) float64 {
	if main == 0 {
		if branch == 0 {
			return 0
		}
		return 100
	}
	return math.Round((branch-main)/main*100*100) / 100
}

// Flags is the judgement half of the diff, separated from the SQL so it can be
// tested against numbers rather than against a database.
//
// The three signals are the three ways a batch goes wrong while every row in it
// is valid: the same file loaded twice, a column that changed units, and a
// source that silently stopped sending rows.
func Flags(rowsMain, rowsBranch float64, sums map[string]SumDiff) []string {
	var flags []string
	for col, s := range sums {
		if math.Abs(s.Pct) >= 30 {
			flags = append(flags, fmt.Sprintf("%s total changed %+.1f%%", col, s.Pct))
		}
	}
	if rowsMain > 0 && math.Abs(rowsBranch-rowsMain*2) < 0.5 {
		flags = append(flags, "row count exactly doubled — a duplicate load")
	}
	if rowsBranch < rowsMain {
		flags = append(flags, fmt.Sprintf("branch has %d fewer rows than production", int64(rowsMain-rowsBranch)))
	}
	// Rows barely moved but a total jumped: the classic unit change (yuan→cents).
	if math.Abs(Pct(rowsMain, rowsBranch)) < 1 {
		for col, s := range sums {
			if s.Pct >= 50 {
				flags = append(flags, fmt.Sprintf(
					"row count barely moved but %s rose %+.1f%% — a unit or definition change", col, s.Pct))
			}
		}
	}
	return flags
}

// Diff compares a branch against production: row counts and the total of every
// numeric column, per table.
func Diff(ctx context.Context, wh *warehouse.Warehouse, name string) (*Report, error) {
	name, err := safeName(name)
	if err != nil {
		return nil, err
	}
	if err := requirePostgres(wh); err != nil {
		return nil, err
	}
	schema := schemaOf(name)

	tables, err := branchTables(ctx, wh, schema)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("branch %q does not exist, or has no tables", name)
	}

	rep := &Report{Branch: name, Schema: schema, Tables: []TableDiff{}}
	for _, t := range tables {
		rowsMain := scalar(ctx, wh, fmt.Sprintf("SELECT count(*)::float8 FROM public.%s", quote(t)))
		rowsBranch := scalar(ctx, wh, fmt.Sprintf("SELECT count(*)::float8 FROM %s.%s", quote(schema), quote(t)))

		sums := map[string]SumDiff{}
		for _, col := range numericColumns(ctx, wh, t) {
			m := scalar(ctx, wh, fmt.Sprintf("SELECT COALESCE(SUM(%s),0)::float8 FROM public.%s", quote(col), quote(t)))
			b := scalar(ctx, wh, fmt.Sprintf("SELECT COALESCE(SUM(%s),0)::float8 FROM %s.%s", quote(col), quote(schema), quote(t)))
			if m == 0 && b == 0 {
				continue
			}
			sums[col] = SumDiff{Main: m, Branch: b, Delta: b - m, Pct: Pct(m, b)}
		}
		flags := Flags(rowsMain, rowsBranch, sums)
		rep.Tables = append(rep.Tables, TableDiff{
			Table: t, RowsMain: int64(rowsMain), RowsBranch: int64(rowsBranch),
			RowsDelta: int64(rowsBranch - rowsMain), RowsPct: Pct(rowsMain, rowsBranch),
			Sums: sums, Flags: flags,
		})
		rep.FlagCount += len(flags)
	}
	rep.Verdict = "no signals"
	if rep.FlagCount > 0 {
		rep.Verdict = "signals found — needs a human"
	}
	return rep, nil
}

func branchTables(ctx context.Context, wh *warehouse.Warehouse, schema string) ([]string, error) {
	res, err := wh.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema=`+literal(schema)+` AND table_type='BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range res.Rows {
		if s := str(row[0]); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// numericColumns lists the columns whose totals are worth comparing.
//
// Key columns are excluded even though they are numeric. Summing a primary key
// is meaningless, and because ids climb, every ordinary incremental load makes
// that sum jump by an arbitrary amount — a 66744% "change" on a load of sixty
// rows, in the first run of this. A gate that flags every normal load is a gate
// people learn to click past, which costs more than having no gate at all.
func numericColumns(ctx context.Context, wh *warehouse.Warehouse, table string) []string {
	res, err := wh.Query(ctx, `SELECT c.column_name FROM information_schema.columns c
		WHERE c.table_schema='public' AND c.table_name=`+literal(table)+`
		AND c.data_type IN ('smallint','integer','bigint','numeric','real','double precision','money')
		AND c.column_name NOT IN (
			SELECT kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON tc.constraint_name = kcu.constraint_name
			 AND tc.table_schema = kcu.table_schema
			WHERE tc.table_schema='public' AND tc.table_name=`+literal(table)+`
			  AND tc.constraint_type IN ('PRIMARY KEY','FOREIGN KEY','UNIQUE'))
		ORDER BY c.ordinal_position`)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, row := range res.Rows {
		if s := str(row[0]); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func scalar(ctx context.Context, wh *warehouse.Warehouse, q string) float64 {
	res, err := wh.Query(ctx, q)
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	switch v := res.Rows[0][0].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return 0
	}
}

// Promote replaces production's data with the branch's, in one transaction.
//
// The data is swapped, not the tables: production keeps its own constraints,
// indexes and grants, which a branch copy made with CREATE TABLE AS does not
// carry. One transaction is not a detail — without it, a failure on the third
// of five tables leaves production with two tables replaced, one emptied, and
// no way to tell which state it is in.
func Promote(ctx context.Context, wh *warehouse.Warehouse, name string) (map[string]any, error) {
	name, err := safeName(name)
	if err != nil {
		return nil, err
	}
	if err := requirePostgres(wh); err != nil {
		return nil, err
	}
	schema := schemaOf(name)
	tables, err := branchTables(ctx, wh, schema)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("branch %q does not exist", name)
	}
	err = wh.Apply(ctx, func(tx *sql.Tx) error {
		for _, t := range tables {
			if _, err := tx.ExecContext(ctx, "DELETE FROM public."+quote(t)); err != nil {
				return fmt.Errorf("clear %s: %w", t, err)
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO public.%s SELECT * FROM %s.%s",
				quote(t), quote(schema), quote(t))); err != nil {
				return fmt.Errorf("load %s: %w", t, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "branch": name, "promoted_tables": tables}, nil
}

// Discard drops the branch schema.
func Discard(ctx context.Context, wh *warehouse.Warehouse, name string) (map[string]any, error) {
	name, err := safeName(name)
	if err != nil {
		return nil, err
	}
	if err := requirePostgres(wh); err != nil {
		return nil, err
	}
	schema := schemaOf(name)
	if _, err := wh.Exec(ctx, "DROP SCHEMA IF EXISTS "+quote(schema)+" CASCADE"); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "discarded": schema}, nil
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
