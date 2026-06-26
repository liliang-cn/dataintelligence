// Package ui is the embedded web console: server-rendered Go templates + htmx +
// Alpine + GSAP + Tailwind, all go:embed'd into the di binary and served by
// `di serve` at /ui. No Node, no build step, no separate deploy — the console
// ships and runs inside the same single binary, offline.
package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/flow"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/nleval"
	"github.com/liliang-cn/dataintelligence/obs"
)

//go:embed templates/*.html static/*
var assets embed.FS

// UI renders the console over the shared engine + governance core.
type UI struct {
	Eng *engine.Engine
	Pol governance.Policy
	Fe  *flow.Engine
	tpl *template.Template
}

// New parses the embedded templates.
func New(eng *engine.Engine, pol governance.Policy, fe *flow.Engine) (*UI, error) {
	tpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &UI{Eng: eng, Pol: pol, Fe: fe, tpl: tpl}, nil
}

// Mount registers the console routes on mux under /ui.
func (u *UI) Mount(mux *http.ServeMux) {
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /ui", u.playground)
	mux.HandleFunc("GET /ui/", u.playground)
	mux.HandleFunc("POST /ui/query", u.query)
	mux.HandleFunc("GET /ui/model", u.model)
	mux.HandleFunc("GET /ui/eval", u.eval)
	mux.HandleFunc("GET /ui/runs", u.runs)
	mux.HandleFunc("POST /ui/runs/{id}/{action}", u.runAction)
	mux.HandleFunc("GET /ui/traces", u.traces)
}

// page wraps page data with the nav-active marker shared by the layout.
func page(active string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	data["Active"] = active
	return data
}

// --- Query playground ---

type metricView struct{ Name, Description string }

func (u *UI) playground(w http.ResponseWriter, _ *http.Request) {
	var metrics []metricView
	for i := range u.Eng.Model.Metrics {
		m := &u.Eng.Model.Metrics[i]
		metrics = append(metrics, metricView{m.Name, m.Description})
	}
	var dims []string
	for i := range u.Eng.Model.Dimensions {
		dims = append(dims, u.Eng.Model.Dimensions[i].Name)
	}
	u.render(w, "playground.html", page("query", map[string]any{
		"Metrics": metrics, "Dimensions": dims,
		"Roles": []string{"analyst", "finance", "manager", "admin"},
	}))
}

func (u *UI) query(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	q := semantic.Query{Metrics: r.Form["metrics"], GroupBy: r.Form["dims"], TimeGrain: r.FormValue("grain")}
	role := r.FormValue("role")
	if role == "" {
		role = "analyst"
	}
	if len(q.Metrics) == 0 {
		u.render(w, "result.html", map[string]any{"Error": "pick at least one metric"})
		return
	}
	p := governance.Principal{User: "ui", Role: role, Attrs: map[string]string{"region": "South"}}
	ans, err := governance.Query(r.Context(), u.Eng, q, p, u.Pol)
	if err != nil {
		u.render(w, "result.html", map[string]any{"Error": err.Error(), "Role": role})
		return
	}
	u.render(w, "result.html", map[string]any{
		"Columns": ans.Columns, "Rows": ans.Rows, "SQL": ans.SQL,
		"EstBytes": ans.EstBytes, "ExecMs": ans.ExecMs, "TraceID": ans.TraceID, "Role": role,
	})
}

// --- Model browser ---

type modelMetric struct {
	Name, Description, Additivity string
	Synonyms, Roles               []string
}
type modelDim struct{ Name, Entity, Type string }

func (u *UI) model(w http.ResponseWriter, _ *http.Request) {
	var metrics []modelMetric
	for i := range u.Eng.Model.Metrics {
		m := &u.Eng.Model.Metrics[i]
		metrics = append(metrics, modelMetric{m.Name, m.Description, u.Eng.Model.Additivity(m.Name), m.Synonyms, m.Roles})
	}
	var dims []modelDim
	for i := range u.Eng.Model.Dimensions {
		d := &u.Eng.Model.Dimensions[i]
		dims = append(dims, modelDim{d.Name, d.Entity, d.Type})
	}
	issues := semantic.Lint(u.Eng.Model)
	u.render(w, "model.html", page("model", map[string]any{
		"Metrics": metrics, "Dimensions": dims, "Issues": issues,
		"Entities": len(u.Eng.Model.Entities), "Joins": len(u.Eng.Model.Joins),
	}))
}

// --- Eval dashboard ---

type evalRunView struct{ RunID, AccPct, Passed, Total, At string }

func (u *UI) eval(w http.ResponseWriter, r *http.Request) {
	raw, err := nleval.History(r.Context(), u.Eng.WH, 20)
	if err != nil {
		u.render(w, "eval.html", page("eval", map[string]any{"Error": err.Error()}))
		return
	}
	// Format display values in Go so the template does no arithmetic.
	runs := make([]evalRunView, 0, len(raw))
	for _, m := range raw {
		runs = append(runs, evalRunView{
			RunID:  fmt.Sprint(m["run_id"]),
			AccPct: fmt.Sprintf("%.0f%%", toFloat(m["accuracy"])*100),
			Passed: fmt.Sprint(m["passed"]),
			Total:  fmt.Sprint(m["total"]),
			At:     fmt.Sprint(m["at"]),
		})
	}
	u.render(w, "eval.html", page("eval", map[string]any{"Runs": runs}))
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	default:
		var f float64
		_, _ = fmt.Sscan(fmt.Sprint(v), &f)
		return f
	}
}

// --- Runs (DataFlow + write-back) ---

func (u *UI) runs(w http.ResponseWriter, r *http.Request) {
	u.render(w, "runs.html", page("runs", map[string]any{"Runs": u.runList(r)}))
}

func (u *UI) runAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	switch action {
	case "approve":
		_, _ = u.Fe.Approve(r.Context(), id)
	case "reject":
		_, _ = u.Fe.Reject(r.Context(), id)
	case "rollback":
		_, _ = u.Fe.Rollback(r.Context(), id)
	}
	// Re-render the list fragment so htmx swaps the updated table in place.
	u.render(w, "runs_list.html", map[string]any{"Runs": u.runList(r)})
}

func (u *UI) runList(r *http.Request) []*flow.Run {
	runs, err := u.Fe.List(r.Context())
	if err != nil {
		return nil
	}
	return runs
}

// --- Traces ---

func (u *UI) traces(w http.ResponseWriter, r *http.Request) {
	ts, err := obs.List(r.Context(), u.Eng.WH, 40)
	if err != nil {
		u.render(w, "traces.html", page("traces", map[string]any{"Error": err.Error()}))
		return
	}
	u.render(w, "traces.html", page("traces", map[string]any{"Traces": ts}))
}

func (u *UI) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}
