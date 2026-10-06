package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServesAppAndFallsBack(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("other")) })

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	for _, p := range []string{"/", "/app/consult", "/app/chat/anything"} {
		if r := get(p); r.Code != 200 || !strings.Contains(r.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: want the app's index.html, got %d %.80q", p, r.Code, r.Body.String())
		}
	}
	if r := get("/assets/missing.js"); r.Code != 404 {
		t.Fatalf("a missing asset must 404, not fall back to index.html: got %d", r.Code)
	}
	if r := get("/healthz"); r.Body.String() != "other" {
		t.Fatalf("routes outside the app must reach the rest of the mux, got %q", r.Body.String())
	}
}
