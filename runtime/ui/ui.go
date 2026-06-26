// Package ui is the embedded web console: server-rendered Go templates + htmx +
// Alpine + GSAP + Tailwind, all go:embed'd into the di binary and served by
// `di serve` at /ui. No Node, no build step, no separate deploy — the console
// ships and runs inside the same single binary, offline.
package ui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
)

//go:embed templates/*.html static/*
var assets embed.FS

// UI renders the console over the shared engine + governance core.
type UI struct {
	Eng *engine.Engine
	Pol governance.Policy
	tpl *template.Template
}

// New parses the embedded templates.
func New(eng *engine.Engine, pol governance.Policy) (*UI, error) {
	tpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &UI{Eng: eng, Pol: pol, tpl: tpl}, nil
}

// Mount registers the console routes on mux under /ui.
func (u *UI) Mount(mux *http.ServeMux) {
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /ui", u.playground)
	mux.HandleFunc("GET /ui/", u.playground)
	mux.HandleFunc("POST /ui/query", u.query)
}

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
	u.render(w, "playground.html", map[string]any{
		"Metrics": metrics, "Dimensions": dims,
		"Roles": []string{"analyst", "finance", "manager", "admin"},
	})
}

func (u *UI) query(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	q := semantic.Query{
		Metrics: r.Form["metrics"], GroupBy: r.Form["dims"], TimeGrain: r.FormValue("grain"),
	}
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
		"EstBytes": ans.EstBytes, "ExecMs": ans.ExecMs, "TraceID": ans.TraceID,
		"Role": role, "Metrics": q.Metrics,
	})
}

func (u *UI) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}
