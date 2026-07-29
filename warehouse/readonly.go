package warehouse

import (
	"fmt"
	"strings"
)

// ReadOnly rejects anything that is not a single read.
//
// This is the cheap outer check, not the guarantee. The guarantee is the
// read-only transaction the query runs in and, in any real deployment, a
// least-privilege login that cannot write at all. A keyword screen alone is a
// game of whack-a-mole against every engine's extensions, and it is the kind of
// control that looks convincing right up until it isn't.
//
// What it does buy: a clear, immediate error naming the problem, instead of a
// permission failure from the database three layers down that a caller cannot
// interpret.
func ReadOnly(sql string) error {
	s := strings.TrimSpace(sql)
	if s == "" {
		return fmt.Errorf("empty query")
	}
	// Strip a leading comment block: "-- …" and "/* … */" can otherwise hide the
	// real first keyword.
	s = stripLeadingComments(s)
	trimmed := strings.TrimRight(s, "; \t\r\n")
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return fmt.Errorf("read-only: a query must start with SELECT or WITH")
	}
	// Scan with literals and quoted identifiers blanked out. Without this a
	// perfectly ordinary read — searching a notes column for the word "update",
	// or selecting a string containing a semicolon — is refused, and a refusal
	// the caller can see is right about nothing is worse than no check at all.
	scrubbed := strings.ToLower(blankQuoted(trimmed))
	if strings.Contains(scrubbed, ";") {
		return fmt.Errorf("only one statement at a time")
	}
	lower = scrubbed
	// Word-boundary check, so a column named "created_at" does not trip it.
	for _, w := range []string{
		"insert", "update", "delete", "drop", "alter", "create", "truncate",
		"grant", "revoke", "merge", "call", "execute", "attach", "copy", "vacuum",
	} {
		if containsWord(lower, w) {
			return fmt.Errorf("read-only: %q is not allowed", strings.ToUpper(w))
		}
	}
	return nil
}

func stripLeadingComments(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		default:
			return s
		}
	}
}

// containsWord reports whether w appears as a whole word in s (already lower).
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		before := byte(' ')
		if j > 0 {
			before = s[j-1]
		}
		after := byte(' ')
		if end := j + len(w); end < len(s) {
			after = s[end]
		}
		if !isWordByte(before) && !isWordByte(after) {
			return true
		}
		i = j + 1
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// blankQuoted replaces the contents of string literals and quoted identifiers
// with spaces, preserving length and structure so the rest of the statement
// still scans normally. Doubled quotes (” "" “) are the SQL escape and stay
// inside the literal.
func blankQuoted(s string) string {
	out := []byte(s)
	var quote byte
	for i := 0; i < len(out); i++ {
		c := out[i]
		if quote == 0 {
			if c == '\'' || c == '"' || c == '`' {
				quote = c
			}
			continue
		}
		if c == quote {
			if i+1 < len(out) && out[i+1] == quote { // escaped: '' stays literal
				out[i+1] = ' '
				i++
				continue
			}
			quote = 0
			continue
		}
		out[i] = ' '
	}
	return string(out)
}
