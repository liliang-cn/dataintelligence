package corpus

import "strings"

// Retrieval always returns something, and something is not always a citation.
//
// Asked "叉车维护周期" — forklift maintenance intervals — against a corpus whose
// only document explains why revenue is net of refunds, the store returned the
// revenue memo. It was not wrong to: top-k over one document has one answer,
// and the fusion score that orders results is a rank, not a similarity, so
// there is no threshold on it that means "related".
//
// A brief that prints an unrelated passage under the heading "written about it"
// is worse than one that prints nothing, because the reader will assume the
// connection is real — that is the entire reason the passage is there. So
// retrieval needs a floor, and the floor has to be something a reader would
// accept as evidence rather than a tuned number.
//
// The floor used here is shared vocabulary: a passage is a citation only if it
// repeats a term from the question. That is deliberately crude. It will drop a
// passage that answers the question in different words, which is a real loss
// and a visible one — the brief says nothing rather than something wrong. It
// will not invent a connection, which is the failure that costs more.
//
// Single CJK characters are excluded from counting as a shared term. 的, 了 and
// 是 appear in every Chinese document ever written, and one shared character is
// not evidence of anything; a shared two-character run is. Latin words of one
// letter are dropped for the same reason.

// bears reports whether a passage repeats any term from the question, and is
// therefore worth showing as a citation.
func bears(question, passage string) bool {
	want := terms(question)
	if len(want) == 0 {
		return true // nothing to match on; let the ranker decide
	}
	have := terms(passage)
	for t := range want {
		if have[t] {
			return true
		}
	}
	return false
}

// terms is the set of things worth matching on: words of two or more letters,
// and every two-character run within a CJK token.
func terms(s string) map[string]bool {
	out := map[string]bool{}
	toks := tokens(s)
	for i, t := range toks {
		r := []rune(t)
		switch {
		case len(r) >= 2:
			out[t] = true
		case len(r) == 1 && isCJK(r[0]):
			// One character is not evidence; the pair it forms with its
			// neighbour is, and CJK tokenizes to one character at a time here.
			if i+1 < len(toks) {
				n := []rune(toks[i+1])
				if len(n) == 1 && isCJK(n[0]) {
					out[t+toks[i+1]] = true
				}
			}
		}
	}
	return out
}

func isCJK(r rune) bool { return r >= 0x3400 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF }

// trimmed is strings.TrimSpace, named so the filter below reads as one thought.
func trimmed(s string) string { return strings.TrimSpace(s) }
