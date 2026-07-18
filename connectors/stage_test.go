package connectors

import "testing"

func TestSafeCol_AcceptsCJKRejectsUnsafe(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"门店", true},     // Chinese — the whole point
		{"销售额", true},    // Chinese
		{"order_id", true}, // ASCII
		{"金额(元)", true},   // Chinese + parens, still Postgres-quotable
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

func TestQuoteIdent_DoublesQuotes(t *testing.T) {
	if got := quoteIdent("门店"); got != `"门店"` {
		t.Errorf("quoteIdent(门店) = %s", got)
	}
	if got := quoteIdent(`a"b`); got != `"a""b"` {
		t.Errorf("quoteIdent(a\"b) = %s, want a\"\"b quoted", got)
	}
}
