// Package web is the web console: a React app (this directory; `make web`
// builds it) whose dist/ is committed and embedded, so `go build` needs no Node.
//
// It is served at / and /app/… (client-side routes fall back to index.html).
// The older htmx console stays at /ui.
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the built app. Paths that name a file get the file (hashed
// assets are cached for a year); every other path gets index.html, which the
// app routes itself.
func Handler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web console not built: run `make web`", http.StatusNotFound)
		})
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(root, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// Mount registers the console on mux: / exactly, the app's own routes under
// /app/, and its assets. Everything else on mux is left alone.
func Mount(mux *http.ServeMux) {
	h := Handler()
	mux.Handle("GET /{$}", h)
	mux.Handle("GET /app/", h)
	mux.Handle("GET /app", h)
	mux.Handle("GET /assets/", h)
	mux.Handle("GET /favicon.svg", h)
}
