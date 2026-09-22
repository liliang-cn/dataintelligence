package board

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Fence serializes the board the way @ai-gui/plugin-dashboard reads it: one
// ```dashboard block holding the definition as JSON.
//
// The renderer validates and, on any violation, renders nothing — not a
// degraded board, nothing. So the limits are enforced while building, and this
// only writes.
func (b *Board) Fence() (string, error) {
	doc, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("board: serialize: %w", err)
	}
	return "```dashboard\n" + string(doc) + "\n```", nil
}

// Markdown is the fence with the provenance around it: a board is handed to
// somebody who was not there when it was built, and the two things they will
// ask are what the numbers mean and who said so.
func (b *Board) Markdown(modelHash, signedBy, signedAt, note string) (string, error) {
	fence, err := b.Fence()
	if err != nil {
		return "", err
	}
	var s strings.Builder
	if b.Title != "" {
		fmt.Fprintf(&s, "# %s\n\n", b.Title)
	}
	switch {
	case signedBy != "":
		fmt.Fprintf(&s, "Definitions from model `%s`, approved by %s on %s", modelHash, signedBy, signedAt)
		if note != "" {
			fmt.Fprintf(&s, " (%s)", note)
		}
		s.WriteString(".\n\n")
	case modelHash != "":
		// Said plainly and once. A board is the artefact people stop
		// checking, so the line that says nobody checked it belongs above the
		// numbers rather than in a footnote.
		fmt.Fprintf(&s, "**Nobody has approved the definitions on this board.** "+
			"It was built from model `%s`, which was never promoted through the registry.\n\n", modelHash)
	}
	s.WriteString(fence)
	s.WriteString("\n")
	if n := b.refused(); n > 0 {
		fmt.Fprintf(&s, "\n%d panel(s) show a refusal instead of numbers — "+
			"the caller's role may not read what they ask for.\n", n)
	}
	return s.String(), nil
}

// Refused is how many panels carry a refusal instead of numbers.
func (b *Board) Refused() int { return b.refused() }

func (b *Board) refused() int {
	n := 0
	for _, p := range b.Panels {
		if p.Error != "" {
			n++
		}
	}
	return n
}
