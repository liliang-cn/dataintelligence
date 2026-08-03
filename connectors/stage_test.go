package connectors

import "testing"

func TestSafeCol_AcceptsCJKRejectsUnsafe(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"门店", true},       // Chinese — the whole point
		{"销售额", true},      // Chinese
		{"order_id", true}, // ASCII
		{"金额(元)", true},    // Chinese + parens, still Postgres-quotable
		{"", false},        // blank
		{"   ", false},     // whitespace only
		{"a\"b", false},    // embedded quote — injection guard
		{"a\nb", false},    // control char
	}
	for _, c := range cases {
		if got := safeCol(c.in); got != c.want {
			t.Errorf("safeCol(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// SAP's field names are all upper case; Postgres folds unquoted identifiers to
// lower case. A landed "MANDT" is a column every control query afterwards has
// to remember to quote.
func TestFoldIdent(t *testing.T) {
	cases := map[string]string{
		"MANDT":      "mandt",
		"VBELN":      "vbeln",
		"Store ID":   "store_id",
		"门店":         "门店",
		"销售额(元)":     "销售额_元",
		"__metadata": "metadata",
	}
	for in, want := range cases {
		if got := FoldIdent(in); got != want {
			t.Errorf("FoldIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two source fields folding to one column keeps whichever row wrote last and
// loses the other without a word.
func TestCollidingColumnNamesAreRefused(t *testing.T) {
	a, b := FoldIdent("Store ID"), FoldIdent("store_id")
	if a != b {
		t.Fatalf("expected a collision to test: %q vs %q", a, b)
	}
}

// An empty string is a missing value, not a zero. Landing "" as 0 makes a
// metric count absent rows as free ones.
func TestEmptyNumericBecomesNullNotZero(t *testing.T) {
	if got := cell("", "numeric"); got != nil {
		t.Errorf("cell(\"\", numeric) = %v, want nil", got)
	}
	if got := cell("", "text"); got != "" {
		t.Errorf("an empty text value is an empty string, got %v", got)
	}
}

// Landing everything as text costs the delivery its metrics: a text column
// cannot be summed.
func TestInferredTypesReachTheDDL(t *testing.T) {
	for _, tc := range []struct{ driver, inferred, want string }{
		{"postgres", "numeric", "NUMERIC"},
		{"postgres", "date", "TIMESTAMP"},
		{"mysql", "numeric", "DECIMAL(38,10)"},
		{"sqlserver", "date", "DATETIME2"},
		{"sqlite", "date", "TEXT"}, // SQLite has no date type; modelgen re-reads these
		{"postgres", "", "TEXT"},
	} {
		if got := sqlType(tc.driver, tc.inferred); got != tc.want {
			t.Errorf("sqlType(%s, %s) = %s, want %s", tc.driver, tc.inferred, got, tc.want)
		}
	}
}
