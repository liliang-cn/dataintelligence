package warehouse

import (
	"strings"
	"testing"
)

func TestReadOnlyAcceptsReads(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1",
		"select city, count(*) from stores group by city",
		"WITH x AS (SELECT 1 AS n) SELECT n FROM x",
		"SELECT 1;",
		"  \n SELECT 1 \n ",
		// A column or alias whose name merely contains a banned word is a read.
		"SELECT created_at, updated_at, deleted_flag FROM t",
		"SELECT * FROM t WHERE note = 'please update your address'",
		"-- a leading comment\nSELECT 1",
	} {
		if err := ReadOnly(sql); err != nil {
			t.Errorf("ReadOnly(%q) = %v, want nil", sql, err)
		}
	}
}

func TestReadOnlyRejectsWritesAndStacking(t *testing.T) {
	for sql, want := range map[string]string{
		"DELETE FROM stores":                    "SELECT or WITH",
		"UPDATE stores SET city='x'":            "SELECT or WITH",
		"":                                      "empty",
		"   ":                                   "empty",
		"SELECT 1; DROP TABLE stores":           "one statement",
		"SELECT 1; SELECT 2":                    "one statement",
		"/* sneaky */ UPDATE stores SET city=1": "SELECT or WITH",
		"-- hide\nDROP TABLE stores":            "SELECT or WITH",
		// Starts as a read, then writes: caught by the keyword screen.
		"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x": "INSERT",
		"SELECT * FROM t INTO OUTFILE '/tmp/x'":              "", // any error will do
	} {
		err := ReadOnly(sql)
		if err == nil {
			if want == "" {
				continue // documented as best-effort; the read-only tx is the guarantee
			}
			t.Errorf("ReadOnly(%q) = nil, want an error", sql)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("ReadOnly(%q) = %v, want it to mention %q", sql, err, want)
		}
	}
}

// A write keyword inside a string literal is not a write, and a semicolon
// inside one is not a second statement. Refusing either is a false alarm the
// caller cannot work around and cannot understand.
func TestReadOnlyIgnoresQuotedText(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t WHERE note = 'please update your address'",
		"SELECT 'a;b' AS s",
		"SELECT * FROM t WHERE body LIKE '%DROP TABLE%'",
		"SELECT `delete` FROM t",        // MySQL-quoted identifier
		`SELECT "update" FROM t`,        // Postgres-quoted identifier
		"SELECT 'it''s an update' AS s", // doubled-quote escape
	} {
		if err := ReadOnly(sql); err != nil {
			t.Errorf("ReadOnly(%q) = %v, want nil", sql, err)
		}
	}
	// Blanking literals must not blind the check to a real write outside one.
	if err := ReadOnly("SELECT 'safe' FROM t; DELETE FROM t"); err == nil {
		t.Error("a real second statement should still be refused")
	}
}
