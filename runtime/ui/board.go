package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/board"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/rollout"
)

// The board page and the fence behind it.
//
// Two routes rather than one, and the split is the point: `/ui/board.md`
// returns the markdown the CLI writes, and `/ui/board` is a page that fetches
// it and hands it to the renderer. A reader who wants to check the numbers
// opens the first one and sees exactly what the model layer produced,
// including the SQL. A page that inlined the fence into its own HTML would be
// a second copy of it, and the two could disagree.
//
// The role comes from the query string here because the console is already
// behind whatever the deployment put in front of it; a deployment that wants
// the board to follow a token wires it the way the MCP server does.

type boardPage struct {
	Title     string
	Database  string
	Role      string
	ModelHash string
	SignedBy  string
	SignedAt  string
	SignNote  string
	Refused   int
	Failed    int
	FenceURL  string
	Dark      bool
}

func (u *UI) boardPage(w http.ResponseWriter, r *http.Request) {
	role := roleOf(r)
	b, err := u.buildBoard(r.Context(), r, role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := boardPage{
		Title:     boardTitle(u, r),
		Database:  u.Eng.WH.Driver(),
		Role:      role,
		ModelHash: u.Eng.ModelHash,
		Refused:   b.Refused(),
		Failed:    b.Failed(),
		// The page supplies the title and the approval line itself, in the
		// reader's language and above the numbers. Asking for the bare fence
		// stops both appearing twice — once from the page and once from the
		// markdown the CLI writes for a reader who has no page.
		FenceURL: fenceURL(r, role),
		Dark:     r.URL.Query().Get("theme") == "dark",
	}
	// Who approved the definitions this board rests on. Absent is not an
	// error: a deployment that has never used the registry is the ordinary
	// case, and the page says so where the numbers are.
	if reg := u.registry(); reg != nil {
		if a, found, err := reg.Signature(r.Context(), u.Eng.ModelHash); err == nil && found && a.Promoted {
			p.SignedBy, p.SignedAt, p.SignNote = a.SignedBy, a.SignedAt, a.Note
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.tpl.ExecuteTemplate(w, "board", p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (u *UI) boardFence(w http.ResponseWriter, r *http.Request) {
	role := roleOf(r)
	b, err := u.buildBoard(r.Context(), r, role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("fence") == "1" {
		fence, err := b.Fence()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(fence))
		return
	}
	by, at, note := "", "", ""
	if reg := u.registry(); reg != nil {
		if a, found, err := reg.Signature(r.Context(), u.Eng.ModelHash); err == nil && found && a.Promoted {
			by, at, note = a.SignedBy, a.SignedAt, a.Note
		}
	}
	doc, err := b.Markdown(u.Eng.ModelHash, by, at, note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(doc))
}

// buildBoard runs the panels as the caller.
//
// `?layout=` carries a board somebody else assembled — an agent through the
// MCP tool, most often — so that board has a URL. Without it the proposal is
// used, and the proposal reads the trail: what this deployment's own people
// ask for. See board/layout.go for why the layout is in the link rather than
// in a table.
func (u *UI) buildBoard(ctx context.Context, r *http.Request, role string) (*board.Board, error) {
	if u.Eng == nil || !u.Eng.Governed() {
		return nil, fmt.Errorf("this database has no semantic model, so there are no metrics to put on a board")
	}
	var panels []board.Panel
	if enc := strings.TrimSpace(r.URL.Query().Get("layout")); enc != "" {
		var err error
		if panels, err = board.DecodeLayout(enc); err != nil {
			return nil, err
		}
	} else {
		panels = board.ProposeFor(ctx, u.Eng, u.Eng.Model)
	}
	return board.Build(ctx, u.Eng,
		governance.Principal{User: "console", Role: role},
		u.Pol, boardTitle(u, r), panels)
}

// boardTitle lets a link name the board it carries. A dashboard somebody sent
// is about something, and "Board" is not that.
func boardTitle(u *UI, r *http.Request) string {
	if t := strings.TrimSpace(r.URL.Query().Get("title")); t != "" {
		return t
	}
	return titleOf(u)
}

// registry is the model registry on the engine's own warehouse handle, or nil
// when the table cannot be reached — a read-only warehouse, most often, which
// is the right configuration for a console and the wrong one for a ledger.
func (u *UI) registry() *rollout.Registry {
	if u.Eng == nil || u.Eng.WH == nil {
		return nil
	}
	return rollout.New(u.Eng.WH, nowUTC)
}

// fenceURL points the page at the fence for the board it is showing. Dropping
// the layout here would have the page fetch a different board from the one its
// heading describes — the two routes exist so they can be compared, and a
// comparison of two different boards is worse than no comparison.
func fenceURL(r *http.Request, role string) string {
	q := url.Values{"fence": {"1"}, "role": {role}}
	if l := strings.TrimSpace(r.URL.Query().Get("layout")); l != "" {
		q.Set("layout", l)
	}
	if t := strings.TrimSpace(r.URL.Query().Get("title")); t != "" {
		q.Set("title", t)
	}
	return "/ui/board.md?" + q.Encode()
}

func titleOf(u *UI) string {
	if u.Eng != nil && u.Eng.Model != nil && u.Eng.Model.Name != "" {
		return u.Eng.Model.Name
	}
	return "Board"
}

// roleOf reads the role the console is browsing as. It is deliberately visible
// in the URL: the board's whole demonstration is that the same board shows
// different things to different roles, and hiding the switch would make that
// look like a bug.
func roleOf(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("role")); v != "" {
		return v
	}
	return "analyst"
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
