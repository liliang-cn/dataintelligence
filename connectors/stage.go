package connectors

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Stage lands a Batch into a freshly (re)created text-columned table. It is a
// generic primitive — it knows nothing about the data's meaning, only its shape.
func Stage(ctx context.Context, wh *warehouse.Warehouse, table string, b Batch) (int, error) {
	if !ident(table) {
		return 0, fmt.Errorf("invalid table %q", table)
	}
	var cols []string
	for _, f := range b.Schema.Fields {
		if ident(f.Name) {
			cols = append(cols, f.Name)
		}
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("source for %q produced no usable columns", table)
	}
	defs := make([]string, len(cols))
	qc := make([]string, len(cols))
	ph := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = `"` + c + `" text`
		qc[i] = `"` + c + `"`
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	if _, err := wh.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, table)); err != nil {
		return 0, err
	}
	if _, err := wh.Exec(ctx, fmt.Sprintf(`CREATE TABLE "%s" (%s)`, table, strings.Join(defs, ", "))); err != nil {
		return 0, err
	}
	stmt := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`, table, strings.Join(qc, ", "), strings.Join(ph, ", "))
	n := 0
	for _, r := range b.Rows {
		args := make([]any, len(cols))
		for i, c := range cols {
			args[i] = r[c]
		}
		if _, err := wh.Exec(ctx, stmt, args...); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func ident(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
