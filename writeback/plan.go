package writeback

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// build turns a validated DataChange into parameterized SQL. Identifiers come
// only from the allowlist (validated) and are quoted; every value is a bind
// parameter — there is no string interpolation of user data.
func build(d *DataChange) (string, []any, error) {
	switch d.Op {
	case OpInsert:
		cols, args, ph := setClause(d.Set, 0)
		sql := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
			quoteIdent(d.Table), strings.Join(quoteAll(cols), ", "), strings.Join(ph, ", "))
		return sql, args, nil
	case OpUpdate:
		cols, args, _ := setClause(d.Set, 0)
		sets := make([]string, len(cols))
		for i, c := range cols {
			sets[i] = fmt.Sprintf("%s = $%d", quoteIdent(c), i+1)
		}
		where, wargs, err := whereClause(d.Where, len(args))
		if err != nil {
			return "", nil, err
		}
		sql := fmt.Sprintf(`UPDATE %s SET %s%s`, quoteIdent(d.Table), strings.Join(sets, ", "), where)
		return sql, append(args, wargs...), nil
	case OpDelete:
		where, wargs, err := whereClause(d.Where, 0)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf(`DELETE FROM %s%s`, quoteIdent(d.Table), where), wargs, nil
	}
	return "", nil, fmt.Errorf("unknown op %q", d.Op)
}

// buildSelect builds the before-image SELECT for an update/delete (current state
// of the rows the change would touch) — doubles as the affected-row preview.
func buildSelect(d *DataChange) (string, []any, error) {
	where, args, err := whereClause(d.Where, 0)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf(`SELECT * FROM %s%s`, quoteIdent(d.Table), where), args, nil
}

// setClause returns sorted columns, their values, and positional placeholders.
func setClause(set map[string]any, offset int) (cols []string, args []any, ph []string) {
	for c := range set {
		cols = append(cols, c)
	}
	sort.Strings(cols) // deterministic SQL (caching / tests)
	for i, c := range cols {
		args = append(args, set[c])
		ph = append(ph, fmt.Sprintf("$%d", offset+i+1))
	}
	return cols, args, ph
}

func whereClause(preds []Predicate, offset int) (string, []any, error) {
	if len(preds) == 0 {
		return "", nil, nil
	}
	var parts []string
	var args []any
	n := offset
	for _, p := range preds {
		op := strings.ToLower(p.Operator)
		if op == "in" {
			vals, ok := p.Value.([]any)
			if !ok {
				return "", nil, fmt.Errorf("IN predicate on %q needs a list value", p.Column)
			}
			var ph []string
			for _, v := range vals {
				n++
				ph = append(ph, fmt.Sprintf("$%d", n))
				args = append(args, v)
			}
			parts = append(parts, fmt.Sprintf("%s IN (%s)", quoteIdent(p.Column), strings.Join(ph, ", ")))
			continue
		}
		n++
		parts = append(parts, fmt.Sprintf("%s %s $%d", quoteIdent(p.Column), op, n))
		args = append(args, p.Value)
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = quoteIdent(s)
	}
	return out
}

// Plan validates a data change and produces a dry-run preview: the parameterized
// SQL, the before-image (current rows for update/delete), and the affected-row
// count — enforcing the table's max-affected cap and the red-line wall. It never
// mutates anything.
func Plan(ctx context.Context, wh *warehouse.Warehouse, s *Schema, d *DataChange) (*Preview, error) {
	if err := d.Validate(s); err != nil {
		return nil, err
	}
	sqlStr, args, err := build(d)
	if err != nil {
		return nil, err
	}
	if v := redLineViolation(sqlStr); v != "" {
		return nil, fmt.Errorf("%s", v)
	}
	t := s.Table(d.Table)
	pv := &Preview{SQL: sqlStr, Args: args}

	if d.Op == OpInsert {
		pv.AffectedRows = 1
		pv.Note = "insert 1 row"
		return pv, nil
	}

	selSQL, selArgs, err := buildSelect(d)
	if err != nil {
		return nil, err
	}
	res, err := wh.Query(ctx, selSQL, selArgs...)
	if err != nil {
		return nil, fmt.Errorf("preview select: %w", err)
	}
	pv.AffectedRows = len(res.Rows)
	pv.Before = rowsToMaps(res.Columns, res.Rows)
	if pv.AffectedRows == 0 {
		pv.Note = "no rows match — nothing would change"
	}
	if t.MaxAffected > 0 && pv.AffectedRows > t.MaxAffected {
		return pv, fmt.Errorf("would affect %d rows, over the %d-row cap for %q — narrow the predicate",
			pv.AffectedRows, t.MaxAffected, d.Table)
	}
	return pv, nil
}

func rowsToMaps(cols []string, rows [][]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if i < len(r) {
				m[c] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}

func scanMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = holders[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
