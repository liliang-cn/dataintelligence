package board

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// A board an agent proposed, at a URL somebody can open.
//
// The console rendered one board: the one proposed from the model. An agent
// that assembled a layout through the MCP tool got a fence back and nowhere to
// put it, so the answer to "send me that dashboard" was to paste JSON.
//
// The layout travels in the URL rather than in a table. A stored layout needs
// somewhere to live, an id, an expiry and a cleanup, and the consoles that most
// want this are pointed at read-only warehouses where the registry already
// cannot write. A self-contained link has none of that and can be pasted into a
// chat, which is what actually happens to a dashboard.
//
// It is safe for the same reason the tool is. The layout names metrics and
// dimensions; it carries no SQL, no filter on rows and no identity. Every panel
// is still compiled from the semantic model and executed as whoever opened the
// link, so a crafted URL shows its opener exactly what they were already
// allowed to see — and a panel naming something they may not read comes back as
// a refusal, which is the same thing the honest layout would do.

// EncodeLayout packs panels into a URL-safe string.
//
// Deflate then base64url: a twelve-panel layout is mostly repeated field names
// and compresses to about a third, which keeps the common board inside the
// length every browser and chat client handles without argument.
func EncodeLayout(panels []Panel) (string, error) {
	if len(panels) == 0 {
		return "", fmt.Errorf("board: nothing to encode")
	}
	raw, err := json.Marshal(panels)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// DecodeLayout is EncodeLayout backwards.
//
// maxLayoutBytes caps what a link may expand to. Without it a short URL can
// decompress into megabytes of panels and the console spends the afternoon
// running them; a board nobody would build by hand is not one worth rendering
// for a stranger.
const maxLayoutBytes = 64 << 10

func DecodeLayout(s string) ([]Panel, error) {
	blob, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("board: this is not a layout link: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(blob)), maxLayoutBytes+1))
	if err != nil {
		return nil, fmt.Errorf("board: this layout link is damaged: %w", err)
	}
	if len(raw) > maxLayoutBytes {
		return nil, fmt.Errorf("board: this layout is larger than %d bytes", maxLayoutBytes)
	}
	var panels []Panel
	if err := json.Unmarshal(raw, &panels); err != nil {
		return nil, fmt.Errorf("board: this layout link is damaged: %w", err)
	}
	if len(panels) == 0 {
		return nil, fmt.Errorf("board: this layout link has no panels")
	}
	if len(panels) > maxPanels {
		panels = panels[:maxPanels]
	}
	for i := range panels {
		if len(panels[i].Metrics) == 0 {
			return nil, fmt.Errorf("board: panel %d names no metric", i+1)
		}
	}
	return panels, nil
}
