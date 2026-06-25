// Package runtime is the control plane over HTTP: the execution dashboard (flow
// runs + approve/reject/rollback), governed query, data explorer, and lineage
// (chain-of-change). It's the API behind a future UI.
package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/cache"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/flow"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/nleval"
	"github.com/liliang-cn/dataintelligence/obs"
)

type Server struct {
	eng *engine.Engine
	fe  *flow.Engine
	c   *cache.Cache
}

func NewServer(eng *engine.Engine, fe *flow.Engine) http.Handler {
	s := &Server{eng: eng, fe: fe, c: cache.New(60 * time.Second)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /query", s.query)
	mux.HandleFunc("GET /runs", s.runs)
	mux.HandleFunc("GET /runs/{id}", s.run)
	mux.HandleFunc("POST /runs/{id}/{action}", s.runAction)
	mux.HandleFunc("GET /tables", s.tables)
	mux.HandleFunc("GET /explore", s.explore)
	mux.HandleFunc("GET /lineage", s.lineage)
	mux.HandleFunc("GET /traces", s.traces)
	mux.HandleFunc("GET /traces/{id}", s.trace)
	mux.HandleFunc("GET /cache", s.cacheStats)
	mux.HandleFunc("GET /nleval", s.nleval)
	return mux
}

// nleval serves the NL-accuracy dashboard: recent eval runs (accuracy trend).
func (s *Server) nleval(w http.ResponseWriter, r *http.Request) {
	runs, err := nleval.History(r.Context(), s.eng.WH, 20)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (s *Server) traces(w http.ResponseWriter, r *http.Request) {
	ts, err := obs.List(r.Context(), s.eng.WH, 50)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) trace(w http.ResponseWriter, r *http.Request) {
	t, err := obs.Get(r.Context(), s.eng.WH, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	type mi struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Synonyms    []string `json:"synonyms,omitempty"`
		Roles       []string `json:"roles,omitempty"`
	}
	var out []mi
	for i := range s.eng.Model.Metrics {
		m := &s.eng.Model.Metrics[i]
		out = append(out, mi{m.Name, m.Description, m.Synonyms, m.Roles})
	}
	writeJSON(w, 200, out)
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Metrics []string `json:"metrics"`
		GroupBy []string `json:"group_by"`
		Grain   string   `json:"grain"`
		Limit   int      `json:"limit"`
		Role    string   `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Role == "" {
		body.Role = "analyst"
	}
	q := semantic.Query{Metrics: body.Metrics, GroupBy: body.GroupBy, TimeGrain: body.Grain, Limit: body.Limit}
	p := governance.Principal{User: "api", Role: body.Role}
	key := cache.Key(p.Role, p.Attrs, q)
	res, err := s.c.Query(key, func() (*engine.Answer, error) {
		return governance.Query(r.Context(), s.eng, q, p, governance.DefaultPolicy())
	})
	if err != nil {
		writeErr(w, 403, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"columns": res.Ans.Columns, "rows": res.Ans.Rows, "sql": res.Ans.SQL,
		"cache": map[string]any{"hit": res.Hit, "stale": res.Stale, "age_ms": res.AgeMs},
	})
}

func (s *Server) cacheStats(w http.ResponseWriter, _ *http.Request) {
	h, m, n := s.c.Stats()
	writeJSON(w, 200, map[string]any{"hits": h, "misses": m, "entries": n})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.fe.List(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	run, err := s.fe.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, run)
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	var run *flow.Run
	var err error
	switch action {
	case "approve":
		run, err = s.fe.Approve(r.Context(), id)
	case "reject":
		run, err = s.fe.Reject(r.Context(), id)
	case "rollback":
		run, err = s.fe.Rollback(r.Context(), id)
	default:
		writeErr(w, 400, errString("unknown action "+action))
		return
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, run)
}

func (s *Server) tables(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.WH.Query(r.Context(),
		`SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var names []string
	for _, row := range res.Rows {
		names = append(names, toStr(row[0]))
	}
	writeJSON(w, 200, names)
}

func (s *Server) explore(w http.ResponseWriter, r *http.Request) {
	table := r.URL.Query().Get("table")
	if !safeIdent(table) {
		writeErr(w, 400, errString("invalid table name"))
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	res, err := s.eng.WH.Query(r.Context(), `SELECT * FROM "`+table+`" LIMIT `+strconv.Itoa(limit))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"columns": res.Columns, "rows": res.Rows})
}

// lineage: which flow runs touched a table (chain-of-change), via persisted state.
func (s *Server) lineage(w http.ResponseWriter, r *http.Request) {
	table := r.URL.Query().Get("table")
	res, err := s.eng.WH.Query(r.Context(),
		`SELECT id, status FROM _flow_runs WHERE doc->'state'->>'table' = $1 ORDER BY updated_at DESC`, table)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var out []map[string]any
	for _, row := range res.Rows {
		out = append(out, map[string]any{"run": toStr(row[0]), "status": toStr(row[1])})
	}
	writeJSON(w, 200, map[string]any{"table": table, "touched_by": out})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

type errString string

func (e errString) Error() string { return string(e) }

func toStr(v any) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func safeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

var _ = context.Background
