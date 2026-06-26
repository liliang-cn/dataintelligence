package runtime

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/obs"
)

// V1 is the stable, versioned data-plane API: governed semantic query, NL
// grounding, and discovery. Every request carries an identity (verified bearer
// token, or dev headers when auth is open) that propagates through governance to
// the warehouse. It shares the exact governance/identity/observability core with
// the MCP server — one engine, two contracts.
type V1 struct {
	Eng    *engine.Engine
	Gr     *grounding.Grounder
	Pol    governance.Policy
	Verify auth.TokenVerifier // nil → open (dev): identity from X-DI-* headers
}

// Handler returns the /v1 mux wrapped with recover + trace-context middleware.
func (v *V1) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /v1/readyz", v.readyz)
	mux.HandleFunc("GET /v1/metrics", v.metricsV1)
	mux.HandleFunc("GET /v1/metrics/{name}/dimensions", v.dimensionsV1)
	mux.HandleFunc("POST /v1/query", v.queryV1)
	mux.HandleFunc("POST /v1/ground", v.groundV1)
	mux.HandleFunc("POST /v1/ask", v.askV1)
	return v.middleware(mux)
}

// middleware: panic recovery + continue any inbound W3C trace, then a server span.
func (v *V1) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeErr(w, 500, errString("internal error"))
			}
		}()
		ctx := obs.ExtractHTTP(r.Context(), r.Header)
		ctx, span := obs.Tracer().Start(ctx, "http "+r.Method+" "+r.URL.Path)
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (v *V1) readyz(w http.ResponseWriter, r *http.Request) {
	if _, err := v.Eng.WH.Query(r.Context(), "SELECT 1"); err != nil {
		writeErr(w, 503, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

// principalFrom derives the caller identity. With auth configured, the bearer
// token is verified and claims (role/tenant/region) become the principal; open
// mode reads dev headers. The identity flows to governance → warehouse OBO.
func (v *V1) principalFrom(r *http.Request) (governance.Principal, bool, error) {
	if v.Verify == nil {
		role := orDefault(r.Header.Get("X-DI-Role"), "analyst")
		return governance.Principal{User: "anon", Role: role, Attrs: map[string]string{
			"tenant": r.Header.Get("X-DI-Tenant"), "region": r.Header.Get("X-DI-Region"),
		}}, true, nil
	}
	tok := bearerToken(r)
	if tok == "" {
		return governance.Principal{}, false, errString("missing bearer token")
	}
	ti, err := v.Verify(r.Context(), tok, r)
	if err != nil {
		return governance.Principal{}, false, err
	}
	return governance.Principal{
		User: ti.UserID,
		Role: orDefault(extra(ti, "role"), "analyst"),
		Attrs: map[string]string{
			"tenant": extra(ti, "tenant"), "region": extra(ti, "region"),
		},
	}, true, nil
}

func (v *V1) metricsV1(w http.ResponseWriter, _ *http.Request) {
	type mi struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Synonyms    []string `json:"synonyms,omitempty"`
		Additivity  string   `json:"additivity,omitempty"`
		Roles       []string `json:"roles,omitempty"`
	}
	out := []mi{}
	for i := range v.Eng.Model.Metrics {
		m := &v.Eng.Model.Metrics[i]
		out = append(out, mi{m.Name, m.Description, m.Synonyms, v.Eng.Model.Additivity(m.Name), m.Roles})
	}
	writeJSON(w, 200, map[string]any{"metrics": out})
}

func (v *V1) dimensionsV1(w http.ResponseWriter, r *http.Request) {
	dims, err := v.Eng.Model.DimensionsFor(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, map[string]any{"metric": r.PathValue("name"), "dimensions": dims})
}

func (v *V1) queryV1(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, err)
		return
	}
	var body struct {
		Metrics []string          `json:"metrics"`
		GroupBy []string          `json:"group_by"`
		Where   []semantic.Filter `json:"where"`
		Grain   string            `json:"grain"`
		Limit   int               `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	q := semantic.Query{Metrics: body.Metrics, GroupBy: body.GroupBy, Where: body.Where, TimeGrain: body.Grain, Limit: body.Limit}
	ans, err := governance.Query(r.Context(), v.Eng, q, p, v.Pol)
	if err != nil {
		writeErr(w, 403, err)
		return
	}
	writeJSON(w, 200, answerEnvelope(ans))
}

func (v *V1) groundV1(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		writeErr(w, 400, errString("question is required"))
		return
	}
	q, _, clar, err := v.Gr.Ground(r.Context(), body.Question)
	if err != nil && clar == nil {
		writeErr(w, 422, err)
		return
	}
	if clar != nil {
		writeJSON(w, 200, map[string]any{"clarify": clar.Question, "candidates": clar.Candidates})
		return
	}
	writeJSON(w, 200, map[string]any{"metrics": q.Metrics, "group_by": q.GroupBy, "grain": q.TimeGrain})
}

func (v *V1) askV1(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, err)
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		writeErr(w, 400, errString("question is required"))
		return
	}
	q, _, clar, err := v.Gr.Ground(r.Context(), body.Question)
	if err != nil && clar == nil {
		writeErr(w, 422, err)
		return
	}
	if clar != nil {
		writeJSON(w, 200, map[string]any{"clarify": clar.Question, "candidates": clar.Candidates})
		return
	}
	ans, err := governance.Query(r.Context(), v.Eng, q, p, v.Pol)
	if err != nil {
		writeErr(w, 403, err)
		return
	}
	env := answerEnvelope(ans)
	env["grounded"] = map[string]any{"metrics": q.Metrics, "group_by": q.GroupBy, "grain": q.TimeGrain}
	writeJSON(w, 200, env)
}

func answerEnvelope(ans *engine.Answer) map[string]any {
	return map[string]any{
		"columns":  ans.Columns,
		"rows":     ans.Rows,
		"sql":      ans.SQL,
		"trace_id": ans.TraceID,
		"cost":     map[string]any{"est_rows": ans.EstRows, "est_bytes": ans.EstBytes},
		"timing":   map[string]any{"compile_ms": ans.CompileMs, "execute_ms": ans.ExecMs},
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func extra(ti *auth.TokenInfo, key string) string {
	if ti == nil || ti.Extra == nil {
		return ""
	}
	if s, ok := ti.Extra[key].(string); ok {
		return s
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
