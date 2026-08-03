package nleval

import (
	"strings"
	"testing"
)

// The draft must refuse to be used unchecked. An eval set assembled from the
// system's own past answers passes by construction — it reports high accuracy
// for agreeing with itself, and every wrong answer becomes the required one.
func TestMinedDraftSaysItIsNotGroundTruth(t *testing.T) {
	out := RenderMined("客户A", []Mined{{Question: "营收", Metrics: []string{"revenue"}, Times: 3, Askers: 2}})
	for _, want := range []string{"ANSWERED", "not what is correct", "defends the bug"} {
		if !strings.Contains(out, want) {
			t.Errorf("the draft header must warn: missing %q", want)
		}
	}
}

// "[a b c]" is what the audit writer produced; reading it back wrongly turns
// every mined case into a case with no expectation, which passes silently.
func TestParseListReadsWhatTheAuditWrote(t *testing.T) {
	for in, want := range map[string]int{
		"[revenue orders]": 2,
		"[revenue]":        1,
		"[]":               0,
		"":                 0,
	} {
		if got := len(parseList(in)); got != want {
			t.Errorf("parseList(%q) = %d item(s), want %d", in, got, want)
		}
	}
}
