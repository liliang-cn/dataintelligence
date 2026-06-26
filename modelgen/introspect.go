// Package modelgen turns a live warehouse into a semantic-model draft: introspect
// the schema (tables, columns, keys, foreign keys), then generate entities /
// joins / dimensions / metrics — heuristically, optionally refined by an LLM.
// It is domain-neutral: it reads whatever schema it is pointed at and never
// assumes a particular business. The output is a starting point a human reviews,
// not a finished model.
package modelgen

import (
	"context"
	"strings"

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

// Introspect reads the public-schema tables of a Postgres warehouse, skipping the
// platform's own bookkeeping tables (those prefixed with "_").
func Introspect(ctx context.Context, wh *warehouse.Warehouse) (*Schema, error) {
	res, err := wh.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
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
		if t.Columns, err = columnsOf(ctx, wh, name); err != nil {
			return nil, err
		}
		if t.PrimaryKey, err = primaryKeyOf(ctx, wh, name); err != nil {
			return nil, err
		}
		if t.ForeignKeys, err = foreignKeysOf(ctx, wh, name); err != nil {
			return nil, err
		}
		s.Tables = append(s.Tables, t)
	}
	return &s, nil
}

func columnsOf(ctx context.Context, wh *warehouse.Warehouse, table string) ([]Column, error) {
	res, err := wh.Query(ctx, `SELECT column_name, data_type, is_nullable
		FROM information_schema.columns WHERE table_schema='public' AND table_name=$1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	var cols []Column
	for _, row := range res.Rows {
		cols = append(cols, Column{Name: str(row[0]), Type: str(row[1]), Nullable: str(row[2]) == "YES"})
	}
	return cols, nil
}

func primaryKeyOf(ctx context.Context, wh *warehouse.Warehouse, table string) (string, error) {
	res, err := wh.Query(ctx, `SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
		WHERE tc.constraint_type='PRIMARY KEY' AND tc.table_schema='public' AND tc.table_name=$1
		ORDER BY kcu.ordinal_position LIMIT 1`, table)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", nil
	}
	return str(res.Rows[0][0]), nil
}

func foreignKeysOf(ctx context.Context, wh *warehouse.Warehouse, table string) ([]ForeignKey, error) {
	res, err := wh.Query(ctx, `SELECT kcu.column_name, ccu.table_name, ccu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu ON tc.constraint_name=kcu.constraint_name
		JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name=tc.constraint_name
		WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_schema='public' AND tc.table_name=$1`, table)
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
