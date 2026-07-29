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
