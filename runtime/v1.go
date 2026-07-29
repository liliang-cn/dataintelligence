package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/liliang-cn/dataintelligence/branch"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/obs"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// V1 is the stable, versioned data-plane API: governed semantic query, NL
// grounding, and discovery. Every request carries an identity (verified bearer
// token, or dev headers when auth is open) that propagates through governance to
// the warehouse. It shares the exact governance/identity/observability core with
// the MCP server — one engine, two contracts.
type V1 struct {
	DBs    *engine.Databases
	Pol    governance.Policy
	Verify auth.TokenVerifier // nil → open (dev): identity from X-DI-* headers
}

// resolve picks the database this request is for (X-DI-Database, ?database=,
// or the default) and returns its engine and grounder. An unknown name is a
// 404 naming what is configured, not a confusing failure further down.
func (v *V1) resolve(w http.ResponseWriter, r *http.Request) (*engine.Engine, *grounding.Grounder, bool) {
	eng, gr, err := v.DBs.Resolve(r.Context(), engine.DatabaseFromRequest(r))
	if err != nil {
		writeErr(w, 404, err)
		return nil, nil, false
	}
	return eng, gr, true
}

// resolveGoverned is resolve for the metric-shaped endpoints. An unmodelled
// database has no metrics to serve, and saying so beats the empty list a nil
// model would otherwise produce — "no metrics" and "not modelled yet" mean
// very different things to whoever is looking.
func (v *V1) resolveGoverned(w http.ResponseWriter, r *http.Request) (*engine.Engine, *grounding.Grounder, bool) {
	eng, gr, ok := v.resolve(w, r)
	if !ok {
		return nil, nil, false
	}
	if !eng.Governed() {
		writeErr(w, 409, errString(fmt.Sprintf(
			"database %q has no semantic model — there are no metrics to query. "+
				"Explore it with POST /v1/sql, or generate a model with: di model gen -dsn … -out <model>.yaml",
			orDefault(engine.DatabaseFromRequest(r), v.DBs.Default()))))
		return nil, nil, false
	}
	return eng, gr, true
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
	mux.HandleFunc("GET /v1/databases", v.databasesV1)
	mux.HandleFunc("POST /v1/databases", v.databaseAddV1)
	mux.HandleFunc("DELETE /v1/databases/{id}", v.databaseDeleteV1)
	mux.HandleFunc("GET /v1/tables", v.tablesV1)
	mux.HandleFunc("POST /v1/sql", v.sqlV1)
	mux.HandleFunc("POST /v1/branch", v.branchCreateV1)
	mux.HandleFunc("GET /v1/branch/diff", v.branchDiffV1)
	mux.HandleFunc("POST /v1/branch/promote", v.branchPromoteV1)
	mux.HandleFunc("POST /v1/branch/discard", v.branchDiscardV1)
	return v.middleware(mux)
}

// middleware: panic recovery + continue any inbound W3C trace, then a server span.
func (v *V1) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// The caller still gets a generic 500 — a panic message can carry
				// internals — but swallowing it entirely left "internal error" as
				// the only evidence anything went wrong, which is undebuggable.
				fmt.Fprintf(os.Stderr, "-- panic in %s %s: %v\n%s\n", r.Method, r.URL.Path, rec, debug.Stack())
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
	eng, _, ok := v.resolve(w, r)
	if !ok {
		return
	}
	if _, err := eng.WH.Query(r.Context(), "SELECT 1"); err != nil {
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

func (v *V1) metricsV1(w http.ResponseWriter, r *http.Request) {
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	type mi struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Synonyms    []string `json:"synonyms,omitempty"`
		Additivity  string   `json:"additivity,omitempty"`
		Roles       []string `json:"roles,omitempty"`
	}
	out := []mi{}
	for i := range eng.Model.Metrics {
		m := &eng.Model.Metrics[i]
		out = append(out, mi{m.Name, m.Description, m.Synonyms, eng.Model.Additivity(m.Name), m.Roles})
	}
	writeJSON(w, 200, map[string]any{"metrics": out})
}

func (v *V1) dimensionsV1(w http.ResponseWriter, r *http.Request) {
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	dims, err := eng.Model.DimensionsFor(r.PathValue("name"))
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
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
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
	ans, err := governance.Query(r.Context(), eng, q, p, v.Pol)
	if err != nil {
		writeErr(w, 403, err)
		return
	}
	writeJSON(w, 200, answerEnvelope(ans))
}

func (v *V1) groundV1(w http.ResponseWriter, r *http.Request) {
	_, gr, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	if gr == nil {
		writeErr(w, 503, errString("grounding is unavailable for this database"))
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		writeErr(w, 400, errString("question is required"))
		return
	}
	q, _, clar, err := gr.Ground(r.Context(), body.Question)
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
	eng, gr, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	if gr == nil {
		writeErr(w, 503, errString("grounding is unavailable for this database"))
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		writeErr(w, 400, errString("question is required"))
		return
	}
	q, _, clar, err := gr.Ground(r.Context(), body.Question)
	if err != nil && clar == nil {
		writeErr(w, 422, err)
		return
	}
	if clar != nil {
		writeJSON(w, 200, map[string]any{"clarify": clar.Question, "candidates": clar.Candidates})
		return
	}
	ans, err := governance.Query(r.Context(), eng, q, p, v.Pol)
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

// databasesV1 lists the configured databases and whether each is governed, so a
// product can render a database picker without guessing which mode it will get.
func (v *V1) databasesV1(w http.ResponseWriter, r *http.Request) {
	type dbi struct {
		ID       string `json:"id"`
		Governed bool   `json:"governed"`
		RawSQL   bool   `json:"raw_sql"`
		Default  bool   `json:"default,omitempty"`
		Editable bool   `json:"editable"` // registered at runtime, so removable via the API
	}
	out := []dbi{}
	for _, id := range v.DBs.IDs() {
		d, _ := v.DBs.Def(id)
		out = append(out, dbi{ID: id, Governed: d.Model != "",
			RawSQL: v.DBs.RawSQLAllowed(id), Default: id == v.DBs.Default(),
			Editable: v.DBs.IsRegistered(id)})
	}
	writeJSON(w, 200, map[string]any{
		"databases": out, "default": v.DBs.Default(),
		"can_register": v.DBs.CanRegister(),
	})
}

// tablesV1 lists a database's tables on any supported engine.
func (v *V1) tablesV1(w http.ResponseWriter, r *http.Request) {
	eng, _, ok := v.resolve(w, r)
	if !ok {
		return
	}
	names, err := modelgen.TableNames(r.Context(), eng.WH)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tables": names})
}

// sqlV1 runs one read-only statement against an UNMODELLED database.
//
// It is not an MCP tool and never will be: an agent pointed at a modelled
// warehouse must ask for metrics, and the moment raw SQL sits beside them as a
// tool the model will reach for it. This endpoint serves a trusted first-party
// product exploring a database nobody has modelled yet — the day-one path,
// before there is a semantic layer to go through.
//
// Against a modelled database it is refused unless that database opted in with
// allow_raw_sql. Modelling a warehouse means answers come from declared
// metrics; an open SQL path beside it quietly makes that optional, and the
// refusal says which metric path to use instead.
func (v *V1) sqlV1(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, err)
		return
	}
	id := engine.DatabaseFromRequest(r)
	eng, _, ok := v.resolve(w, r)
	if !ok {
		return
	}
	if !v.DBs.RawSQLAllowed(id) {
		writeErr(w, 403, errString(fmt.Sprintf(
			"database %q has a semantic model: query it with POST /v1/query (metrics + group_by), "+
				"or set allow_raw_sql on that database to permit direct SQL",
			orDefault(id, v.DBs.Default()))))
		return
	}
	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	res, err := eng.WH.QueryReadOnly(r.Context(), body.SQL)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	governance.AuditRawSQL(r.Context(), eng, p, orDefault(id, v.DBs.Default()), body.SQL, res.RowCount)
	writeJSON(w, 200, map[string]any{
		"columns": res.Columns, "rows": res.Rows,
		"row_count": res.RowCount, "truncated": res.Truncated,
		"elapsed_ms": res.Elapsed.Milliseconds(),
	})
}

// --- pre-ingestion branch gate -------------------------------------------
//
// Load a batch into a copy of the affected tables, compare the aggregates with
// production, and only then decide. Row-level checks cannot catch a file
// imported twice or a column that changed units: every row is valid and the
// totals are wrong.
//
// Only the diff is a read. Create, promote and discard change data, so they are
// POSTs a human triggers — and promote, the one that touches production, is
// never exposed as an agent-callable tool anywhere.

func (v *V1) branchCreateV1(w http.ResponseWriter, r *http.Request) {
	eng, ok := v.branchEngine(w, r)
	if !ok {
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Tables []string `json:"tables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, 400, errString("name is required"))
		return
	}
	out, err := branch.Create(r.Context(), eng.WH, body.Name, body.Tables)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (v *V1) branchDiffV1(w http.ResponseWriter, r *http.Request) {
	eng, ok := v.branchEngine(w, r)
	if !ok {
		return
	}
	rep, err := branch.Diff(r.Context(), eng.WH, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (v *V1) branchPromoteV1(w http.ResponseWriter, r *http.Request) {
	v.branchAction(w, r, branch.Promote)
}

func (v *V1) branchDiscardV1(w http.ResponseWriter, r *http.Request) {
	v.branchAction(w, r, branch.Discard)
}

func (v *V1) branchAction(w http.ResponseWriter, r *http.Request,
	fn func(context.Context, *warehouse.Warehouse, string) (map[string]any, error)) {
	eng, ok := v.branchEngine(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, 400, errString("name is required"))
		return
	}
	out, err := fn(r.Context(), eng.WH, body.Name)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (v *V1) branchEngine(w http.ResponseWriter, r *http.Request) (*engine.Engine, bool) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return nil, false
	}
	eng, _, ok := v.resolve(w, r)
	return eng, ok
}

// databaseAddV1 registers a database at runtime — a product's setup wizard,
// where someone types connection details and expects to be querying a minute
// later rather than editing YAML and restarting a service.
//
// It is off unless the config sets databases_file. An endpoint that opens a
// connection string the caller supplies is not something to have on by accident
// on a networked service, and "off by default, with the error saying how to
// turn it on" is the only version of that choice a user can act on.
func (v *V1) databaseAddV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	if !v.DBs.CanRegister() {
		writeErr(w, 403, errString(
			"runtime database registration is disabled — set databases_file in the config to enable it"))
		return
	}
	var body struct {
		ID          string `json:"id"`
		DSN         string `json:"dsn"`
		Model       string `json:"model"`
		AllowRawSQL bool   `json:"allow_raw_sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	def := engine.Def{ID: body.ID, DSN: body.DSN, Model: body.Model, AllowRawSQL: body.AllowRawSQL}
	if err := v.DBs.Register(r.Context(), def); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "id": def.ID, "governed": def.Model != "",
		"raw_sql": v.DBs.RawSQLAllowed(def.ID),
	})
}

func (v *V1) databaseDeleteV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	if err := v.DBs.Unregister(r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": r.PathValue("id")})
}
