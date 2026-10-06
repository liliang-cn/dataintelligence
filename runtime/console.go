package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/liliang-cn/dataintelligence/copilot"
)

// What the web console (runtime/web, served at /) needs beyond the data API.
//
//	GET  /v1/console          title, auth mode, which parts are enabled — no identity needed,
//	                          so the sign-in screen can show the product name
//	GET  /v1/dimensions       the default model's dimensions with their synonyms
//	POST /v1/copilot/stream   {question} → text/event-stream of copilot.StreamEvent
func (v *V1) mountConsole(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/console", v.consoleInfo)
	mux.HandleFunc("GET /v1/dimensions", v.dimensionListV1)
	mux.HandleFunc("POST /v1/copilot/stream", v.copilotStream)
}

func (v *V1) consoleInfo(w http.ResponseWriter, _ *http.Request) {
	title := v.Title
	if title == "" {
		title = v.Engagement
	}
	writeJSON(w, 200, map[string]any{
		"title":   title,
		"auth":    v.AuthMode(),
		"copilot": v.Copilot != nil,
		"consult": v.Consult != nil,
	})
}

func (v *V1) dimensionListV1(w http.ResponseWriter, r *http.Request) {
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	type di struct {
		Name     string   `json:"name"`
		Type     string   `json:"type,omitempty"`
		Synonyms []string `json:"synonyms,omitempty"`
	}
	out := []di{}
	for i := range eng.Model.Dimensions {
		d := &eng.Model.Dimensions[i]
		out = append(out, di{d.Name, d.Type, d.Synonyms})
	}
	writeJSON(w, 200, map[string]any{"dimensions": out})
}

// copilotStream runs the copilot as the caller and streams its progress. It is
// the console's chat; the htmx page at /ui/copilot keeps its own GET stream.
func (v *V1) copilotStream(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, errString(errText(err)))
		return
	}
	if v.Copilot == nil {
		writeErr(w, 404, errString("顾问对话没有启用：服务端没有配置 LLM_BASE_URL / LLM_API_KEY / LLM_MODEL"))
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	q := strings.TrimSpace(body.Question)
	if q == "" {
		writeErr(w, 400, errString("问题是空的"))
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeErr(w, 500, errString("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	send := func(ev copilot.StreamEvent) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	ctx := r.Context()
	if p.User != "" && p.User != "anon" {
		ctx = copilot.WithPrincipal(ctx, p)
	}
	if _, err := v.Copilot.Stream(ctx, q, send); err != nil {
		send(copilot.StreamEvent{Kind: "error", Text: err.Error()})
	}
}
