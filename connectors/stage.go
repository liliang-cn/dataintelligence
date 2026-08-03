package connectors

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// Stage lands a Batch into a freshly (re)created table. It is a generic
// primitive — it knows nothing about the data's meaning, only its shape.
func Stage(ctx context.Context, wh *warehouse.Warehouse, table string, b Batch) (int, error) {
	return StageWithKey(ctx, wh, table, b, nil)
}

// StageWithKey lands a Batch and declares a primary key over the named source
// fields.
//
// A landed table has no key of its own — an HTTP response does not carry one —
// and `di model gen` refuses a table without one, because a compiler that does
// not know a table's grain cannot promise anything about fan-out. So every
// API-sourced table was unmodelable, which is most of the point of landing it.
//
// The source system knows its key and the platform cannot guess it, so the
// manifest declares it. Asserting it here rather than trusting it is the useful
// half: "you told me VBELN+POSNR identifies a row, and it does not" is a
// finding, and it is much cheaper to learn at load time than from a metric that
// double-counts three months later.
func StageWithKey(ctx context.Context, wh *warehouse.Warehouse, table string, b Batch, key []string) (int, error) {
	if !ident(table) {
		return 0, fmt.Errorf("invalid table %q", table)
	}
	d := wh.Dialect()

	type col struct{ src, dst, typ string }
	var cols []col
	used := map[string]bool{}
	for _, f := range b.Schema.Fields {
		if !safeCol(f.Name) {
			continue
		}
		name := FoldIdent(f.Name)
		// Two source fields can fold to one name (SAP's MANDT and mandt, a CSV
		// with "Store ID" and "store_id"). Landing both into one column keeps
		// whichever row wrote last and loses the other without a word.
		if used[name] {
			return 0, fmt.Errorf("source fields %q and something before it both land in column %q — rename one at the source", f.Name, name)
		}
		used[name] = true
		cols = append(cols, col{src: f.Name, dst: name, typ: f.Type})
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("source for %q produced no usable columns", table)
	}

	defs := make([]string, len(cols))
	qc := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = d.QuoteIdent(c.dst) + " " + sqlType(d.Name(), c.typ)
		qc[i] = d.QuoteIdent(c.dst)
	}
	qt := d.QuoteIdent(table)
	if _, err := wh.Exec(ctx, "DROP TABLE IF EXISTS "+qt); err != nil {
		return 0, err
	}
	if _, err := wh.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", qt, strings.Join(defs, ", "))); err != nil {
		return 0, err
	}

	// One INSERT per row is 250 round trips for a demo and half a million for a
	// real extract, which is the difference between a sync that runs nightly and
	// one that never finishes. Postgres caps a statement at 65535 bind
	// parameters, so rows are chunked to stay under it.
	const maxParams = 60000
	perChunk := maxParams / len(cols)
	if perChunk < 1 {
		perChunk = 1
	}

	insert := func(rows []Record) error {
		if len(rows) == 0 {
			return nil
		}
		args := make([]any, 0, len(rows)*len(cols))
		tuples := make([]string, 0, len(rows))
		for _, r := range rows {
			ph := make([]string, len(cols))
			for i, c := range cols {
				ph[i] = d.Placeholder(len(args) + 1)
				args = append(args, cell(r[c.src], c.typ))
			}
			tuples = append(tuples, "("+strings.Join(ph, ", ")+")")
		}
		_, err := wh.Exec(ctx, fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			qt, strings.Join(qc, ", "), strings.Join(tuples, ", ")), args...)
		return err
	}

	n := 0
	for start := 0; start < len(b.Rows); start += perChunk {
		end := start + perChunk
		if end > len(b.Rows) {
			end = len(b.Rows)
		}
		if err := insert(b.Rows[start:end]); err != nil {
			return n, err
		}
		n += end - start
	}

	if len(key) > 0 {
		if err := addKey(ctx, wh, d, qt, table, key); err != nil {
			return n, err
		}
	}
	return n, nil
}

// addKey declares the primary key, and says plainly when the data does not
// support it.
func addKey(ctx context.Context, wh *warehouse.Warehouse, d semantic.Dialect, qt, table string, key []string) error {
	cols := make([]string, len(key))
	for i, k := range key {
		cols[i] = d.QuoteIdent(FoldIdent(k))
	}
	list := strings.Join(cols, ", ")
	for _, c := range cols {
		if _, err := wh.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", qt, c)); err != nil {
			return fmt.Errorf("primary_key %s: %s is null in some rows — it does not identify a row: %w",
				strings.Join(key, ","), c, err)
		}
	}
	if _, err := wh.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)", qt, list)); err != nil {
		dupes, _ := wh.Query(ctx, fmt.Sprintf(
			"SELECT count(*) FROM (SELECT %s FROM %s GROUP BY %s HAVING count(*) > 1) t", list, qt, list))
		if dupes != nil && len(dupes.Rows) > 0 && len(dupes.Rows[0]) > 0 {
			return fmt.Errorf("primary_key %s does not identify a row in %s: %v value(s) occur more than once",
				strings.Join(key, ","), table, dupes.Rows[0][0])
		}
		return fmt.Errorf("primary_key %s on %s: %w", strings.Join(key, ","), table, err)
	}
	return nil
}

// cell prepares one value for its column's type.
//
// An empty string is a missing value, not a zero: landing "" into a numeric
// column fails the whole chunk, and landing it as 0 would make a metric count
// absent rows as free ones.
func cell(v, typ string) any {
	if typ != "" && typ != "text" && strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// sqlType maps an inferred type to the engine's own.
//
// Landing everything as text was simpler and cost the delivery its metrics: a
// column of text cannot be summed, so an ERP extract produced a model with
// dimensions and nothing to measure. Types come from the whole batch, not a
// sample, because Batch already holds every row that is about to be written.
func sqlType(driver, inferred string) string {
	switch driver {
	case "mysql":
		switch inferred {
		case "int":
			return "BIGINT"
		case "numeric":
			return "DECIMAL(38,10)"
		case "date":
			return "DATETIME"
		case "bool":
			return "TINYINT(1)"
		default:
			return "TEXT"
		}
	case "sqlite":
		// SQLite has no date type, and text is what every SQLite tool expects
		// dates to be in. modelgen re-reads these by sampling the data.
		switch inferred {
		case "int":
			return "INTEGER"
		case "numeric":
			return "REAL"
		default:
			return "TEXT"
		}
	case "sqlserver":
		switch inferred {
		case "int":
			return "BIGINT"
		case "numeric":
			return "DECIMAL(38,10)"
		case "date":
			return "DATETIME2"
		case "bool":
			return "BIT"
		default:
			return "NVARCHAR(MAX)"
		}
	default: // postgres, duckdb
		switch inferred {
		case "int":
			return "BIGINT"
		case "numeric":
			return "NUMERIC"
		case "date":
			// timestamp, not timestamptz: a date-only value cast to timestamptz
			// silently acquires the server's offset and can land on the previous
			// day. Dropping an explicit Z is the smaller of the two losses, and
			// an ERP's timestamps are plant-local anyway.
			return "TIMESTAMP"
		case "bool":
			return "BOOLEAN"
		default:
			return "TEXT"
		}
	}
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

// FoldIdent folds a source field name into a stable column name that is safe
// for CJK.
//
// Unicode letters and digits are kept (so "门店" stays "门店"), ASCII letters are
// lower-cased, and any run of other characters collapses to a single
// underscore. Lower-casing is not cosmetic: SAP's field names are all upper
// case, Postgres folds unquoted identifiers to lower case, and a landed
// "MANDT" is a column that every hand-written control query afterwards has to
// remember to quote — including the ones written by the customer's team a year
// later, who will not.
func FoldIdent(s string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		pendingSep = true
	}
	return b.String()
}

// safeCol accepts any non-blank identifier that has no control characters or
// embedded double-quote — i.e. anything Postgres can hold as a quoted Unicode
// identifier. Chinese/CJK column names ("门店", "销售额") are the norm in SMB data,
// so we keep them verbatim (quoted at the DDL/DML layer) rather than dropping
// them the way the ASCII-only `ident` gate did.
func safeCol(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == '"' {
			return false
		}
	}
	return true
}
