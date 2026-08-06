package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	semantic "github.com/liliang-cn/semantic-go"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/liliang-cn/dataintelligence/anchor"
	"github.com/liliang-cn/dataintelligence/branch"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/handover"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/nleval"
	"github.com/liliang-cn/dataintelligence/obs"
	"github.com/liliang-cn/dataintelligence/reconcile"
	"github.com/liliang-cn/dataintelligence/survey"
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
	// Engagement stamps every audit row with the customer this deployment
	// serves, so one deployment can serve several without their trails becoming
	// one another's.
	Engagement string
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
	mux.HandleFunc("POST /v1/databases/{id}/model", v.databaseModelV1)
	mux.HandleFunc("GET /v1/databases/{id}/model", v.databaseModelGetV1)
	mux.HandleFunc("PUT /v1/databases/{id}/model", v.databaseModelPutV1)
	mux.HandleFunc("POST /v1/databases/{id}/survey", v.databaseSurveyV1)
	mux.HandleFunc("POST /v1/databases/{id}/anchor", v.databaseAnchorV1)
	mux.HandleFunc("POST /v1/databases/{id}/eval", v.databaseEvalV1)
	mux.HandleFunc("POST /v1/databases/{id}/report", v.databaseReportV1)
	mux.HandleFunc("GET /v1/databases/{id}/adoption", v.databaseAdoptionV1)
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
		return governance.Principal{User: "anon", Role: role, Engagement: v.Engagement,
			Attrs: map[string]string{
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
		User:       ti.UserID,
		Role:       orDefault(extra(ti, "role"), "analyst"),
		Engagement: v.Engagement,
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
	// Carry the question into the trail. Without it the audit records that
	// somebody asked for these three metrics grouped by month, which is what
	// the system decided, not what the person asked — and the eval set then has
	// to be written from imagination instead of from what people say.
	p.Question = body.Question
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
		// Record the refusal, then report it. Returning early here left the
		// one channel with no semantic layer vouching for it recording only
		// what succeeded — so a `DELETE FROM sales` stopped by the read-only
		// gate left no trace at all, and that attempt is exactly the row
		// somebody goes looking for afterwards.
		governance.AuditRawSQLRefused(r.Context(), eng, p, orDefault(id, v.DBs.Default()), body.SQL, err)
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

// databaseModelV1 drafts a semantic model from a database's live schema and
// switches that database to governed.
//
// This is the one button that turns "connected, exploring with SQL" into
// "answers come from declared metrics", so it belongs where the introspection
// and the compiler already are. The draft is heuristic and says so: it is a
// starting point for someone who knows the business, not a finished model —
// which is why the response reports the metric count rather than implying the
// modelling is done.
func (v *V1) databaseModelV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	dir := v.DBs.ModelsDir()
	if dir == "" || !v.DBs.CanRegister() {
		writeErr(w, 403, errString(
			"model generation is disabled — set databases_file (and optionally models_dir) in the config"))
		return
	}
	def, ok := v.DBs.Def(id)
	if !ok {
		writeErr(w, 404, errString(fmt.Sprintf("unknown database %q", id)))
		return
	}
	eng, _, err := v.DBs.Resolve(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	schema, err := modelgen.Introspect(r.Context(), eng.WH)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	model, issues, err := modelgen.Generate(r.Context(), schema, nil)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	yamlOut, err := modelgen.ToYAML(model)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, err)
		return
	}
	path := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(path, yamlOut, 0o600); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Re-register with the model attached: the database is governed from here,
	// which also means /v1/sql stops working on it. That is the point.
	def.Model = path
	if err := v.DBs.Register(r.Context(), def); err != nil {
		writeErr(w, 400, err)
		return
	}
	notes := []string{}
	for _, is := range issues {
		notes = append(notes, is.Message)
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "database": id, "path": path,
		"tables": len(schema.Tables), "metrics": len(model.Metrics),
		"dimensions": len(model.Dimensions), "entities": len(model.Entities),
		"notes": notes,
	})
}

// databaseModelGetV1 returns the semantic model a database is served with, plus
// its lint issues.
//
// Generating a draft was already possible; reading one back was not, so the only
// way to review or correct a generated model was to find the file on the box and
// edit it there. That is fine for the person who deployed it and impossible for
// anyone else — and reviewing the draft is the whole job. The issues ride along
// because "what is wrong with this model" and "what is this model" are the same
// question while someone is fixing it.
func (v *V1) databaseModelGetV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	def, ok := v.DBs.Def(id)
	if !ok {
		writeErr(w, 404, errString(fmt.Sprintf("unknown database %q", id)))
		return
	}
	if def.Model == "" {
		writeErr(w, 404, errString(fmt.Sprintf(
			"database %q has no semantic model — generate one with POST /v1/databases/%s/model", id, id)))
		return
	}
	raw, err := os.ReadFile(def.Model)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	m, err := semantic.Load(raw)
	if err != nil {
		// The file on disk no longer parses. Say so with the YAML still attached:
		// whoever is looking needs to see the text to fix it.
		writeJSON(w, 200, map[string]any{
			"database": id, "path": def.Model, "yaml": string(raw),
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, 200, map[string]any{
		"database": id, "path": def.Model, "yaml": string(raw),
		"entities": len(m.Entities), "dimensions": len(m.Dimensions), "metrics": len(m.Metrics),
		"issues": lintJSON(m),
	})
}

// databaseModelPutV1 replaces a database's semantic model and reloads it.
//
// The model is parsed and indexed BEFORE anything is written. A semantic model
// is the only thing standing between an agent and the warehouse; writing one
// that does not compile would take the database down, and it would do it at the
// moment someone was trying to improve it. A rejected edit costs a round trip.
//
// The previous version is kept beside the file. Editing a metric's meaning is
// not a formatting change — someone will want the old wording back, and "restore
// from the box" is not something a person on a customer site can do.
func (v *V1) databaseModelPutV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	def, ok := v.DBs.Def(id)
	if !ok {
		writeErr(w, 404, errString(fmt.Sprintf("unknown database %q", id)))
		return
	}
	var body struct {
		YAML string `json:"yaml"`
		// Only names the single metric or dimension this edit is allowed to
		// touch. Optional, and the reason it exists is that the edit is often
		// produced by a model: asked to add one description it returns the whole
		// file, and a file it rewrote is a file it may have quietly changed
		// elsewhere. A dropped metric parses, indexes, and serves — the failure
		// shows up as a question nobody can ask any more, weeks later.
		Only string `json:"only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeErr(w, 400, errString("yaml is required"))
		return
	}

	m, err := semantic.Load([]byte(body.YAML))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := m.Index(); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Only != "" {
		prev, err := os.ReadFile(def.Model)
		if err != nil {
			writeErr(w, 400, errString("cannot scope an edit against a model that isn't there yet"))
			return
		}
		before, err := semantic.Load(prev)
		if err != nil {
			writeErr(w, 400, errString("the model on disk no longer parses; edit it whole"))
			return
		}
		if changed := changedOutside(before, m, body.Only); len(changed) > 0 {
			writeErr(w, 409, errString(fmt.Sprintf(
				"this edit was scoped to %q but also changed: %s", body.Only, strings.Join(changed, ", "))))
			return
		}
	}

	path := def.Model
	if path == "" {
		dir := v.DBs.ModelsDir()
		if dir == "" || !v.DBs.CanRegister() {
			writeErr(w, 403, errString(
				"this database has no model file and models_dir is not configured"))
			return
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, 500, err)
			return
		}
		path = filepath.Join(dir, id+".yaml")
	} else if prev, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".prev", prev, 0o600)
	}
	if err := os.WriteFile(path, []byte(body.YAML), 0o600); err != nil {
		writeErr(w, 500, err)
		return
	}

	// Re-registering is what makes the edit live; without it the process keeps
	// serving the model it loaded at boot and the edit appears to do nothing.
	def.Model = path
	if err := v.DBs.Register(r.Context(), def); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "database": id, "path": path,
		"entities": len(m.Entities), "dimensions": len(m.Dimensions), "metrics": len(m.Metrics),
		"issues": lintJSON(m),
	})
}

// databaseSurveyV1 profiles what is actually in a customer's database.
//
// This is the first thing anyone does on site and until now it was CLI-only,
// which meant the finding that matters most — a feed that stopped three months
// ago — was visible only to whoever had a shell on the box.
//
// The cost bounds are not tuning knobs. A survey runs against production on day
// one, the worst possible moment to be the reason it got slow, so orphan probes
// are off by default here and large tables are sampled.
func (v *V1) databaseSurveyV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	eng, _, err := v.DBs.Resolve(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	var body struct {
		SkipOrphans *bool `json:"skip_orphans"`
		SampleAbove int64 `json:"sample_above"`
		SampleRows  int64 `json:"sample_rows"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	skip := true
	if body.SkipOrphans != nil {
		skip = *body.SkipOrphans
	}
	if body.SampleAbove == 0 {
		body.SampleAbove = 2_000_000
	}
	if body.SampleRows == 0 {
		body.SampleRows = 50_000
	}
	rep, err := survey.Run(r.Context(), eng.WH, id, survey.Options{
		MaxDistinct: 200,
		StaleAfter:  90 * 24 * time.Hour,
		SkipOrphans: skip,
		SampleAbove: body.SampleAbove,
		SampleRows:  body.SampleRows,
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, rep)
}

// databaseAnchorV1 finds which scope of a metric reproduces a figure the
// customer already publishes.
//
// Every control query until now was written by whoever wrote the metric, from
// the same understanding — so they agreed, and agreement is not correctness.
// This is what lets a delivery report say VERIFIED instead of SELF-CONSISTENT.
//
// `figure` is taken as written, not as a number: a customer who writes 97.3%
// has told you the precision they have, and the right window is half of the
// last digit. A fixed tolerance is wrong in both directions — too tight and the
// real scope never appears, too loose and everything matches, which is the same
// as nothing matching but reads like success.
func (v *V1) databaseAnchorV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	var body struct {
		Metric string  `json:"metric"`
		Figure string  `json:"figure"`
		Target float64 `json:"target"`
		Tol    float64 `json:"tol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(body.Metric) == "" {
		writeErr(w, 400, errString("metric is required"))
		return
	}
	target, tol := body.Target, body.Tol
	if body.Figure != "" {
		digits, mult, err := parseFigure(body.Figure)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		target = digits * mult
		if tol == 0 {
			// 精度读在**数字部分**上,再按同一个倍数放大:写「2610.3万」的人
			// 声明的精度是 0.1 万,不是 0.1 元。在 26,103,000 上用 0.05 的容差,
			// 什么都匹配不上,而那看起来和"模型建错了"一模一样。
			tol = anchor.ToleranceOf(digitsOf(body.Figure)) * mult
		}
	}
	res, err := anchor.Search(r.Context(), eng, body.Metric, target, anchor.Options{Tol: tol})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, res)
}

// parseFigure reads a customer-written figure: "97.3%", "2,610.3", "26.1万".
// Returns the digits and the unit multiplier separately, because the precision
// the customer declared lives in the digits and the scale does not.
func parseFigure(s string) (float64, float64, error) {
	t := digitsOf(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(strings.TrimSpace(s), "万"):
		mult = 10_000
	case strings.HasSuffix(strings.TrimSpace(s), "亿"):
		mult = 100_000_000
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("看不懂这个数字 %q", s)
	}
	return f, mult, nil
}

// digitsOf strips grouping commas and the unit suffix, leaving what the
// customer actually wrote as a number.
func digitsOf(s string) string {
	t := strings.TrimSpace(s)
	t = strings.ReplaceAll(t, ",", "")
	for _, suf := range []string{"%", "万", "亿", "元"} {
		t = strings.TrimSuffix(t, suf)
	}
	return strings.TrimSpace(t)
}

// databaseEvalV1 runs the reconciliation set: every metric against a control
// query somebody wrote by hand, and anything that disagrees is a finding.
//
// The set lives beside the model as <model>.recon.yaml. Absent is not an error
// worth a 500 — it is the normal state before anyone has written one — so it
// comes back as an empty result saying where to put it.
func (v *V1) databaseEvalV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	def, ok := v.DBs.Def(id)
	if !ok {
		writeErr(w, 404, errString(fmt.Sprintf("unknown database %q", id)))
		return
	}
	if def.Model == "" {
		writeErr(w, 409, errString("this database has no semantic model, so there is nothing to reconcile"))
		return
	}
	// 对照集就放在模型旁边:同一次交付里两份东西,分开放迟早会对不上。
	path := ""
	if path == "" {
		path = strings.TrimSuffix(def.Model, filepath.Ext(def.Model)) + ".recon.yaml"
	}
	cs, err := reconcile.Load(path)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"database": id, "path": path, "results": []any{},
			"note": fmt.Sprintf("还没有对照集。写一份放在 %s,每条指标配一句人工写的 SQL。", path),
		})
		return
	}
	eng, _, err := v.DBs.Resolve(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	results, err := reconcile.Run(r.Context(), eng.WH, cs, nil)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"database": id, "path": path, "results": results})
}

// databaseReportV1 builds the delivery report: what was modelled, and what
// proves it.
//
// The verdict is the whole point and it is deliberately hard to earn.
// Reconciliation is the load-bearing half — without it the report says so
// rather than printing a shape and letting it read as verification. A document
// that lists twenty metrics and no evidence is not a lighter version of a
// delivery report; it is the thing a delivery report exists to prevent.
func (v *V1) databaseReportV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = v.DBs.Default()
	}
	def, ok := v.DBs.Def(id)
	if !ok {
		writeErr(w, 404, errString(fmt.Sprintf("unknown database %q", id)))
		return
	}
	if def.Model == "" {
		writeErr(w, 409, errString("this database has no semantic model — there is nothing to report yet"))
		return
	}
	eng, _, err := v.DBs.Resolve(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}

	d := &nleval.Delivery{Database: id, Model: def.Model}
	d.Describe(eng.Model)
	for _, is := range semantic.Lint(eng.Model) {
		d.Notes = append(d.Notes, is.String())
	}

	// 对账拿得到就带上,拿不到就空着。**空着会让 Verdict 说不出 VERIFIED**,
	// 那正是想要的:没有对照就没有证据,而一份列了二十个指标、没有证据的文档
	// 不是交付报告的轻量版,它正是交付报告要防的东西。
	if set, err := nleval.LoadReconSet(nleval.ReconPathFor(def.Model)); err == nil {
		if rep, err := nleval.Reconcile(r.Context(), eng, set); err == nil {
			d.Recon = rep
			d.Uncovered = rep.Uncovered(eng.Model)
		}
	}

	var md strings.Builder
	d.WriteMarkdown(&md)
	writeJSON(w, 200, map[string]any{
		"database": id, "verdict": d.Verdict(), "markdown": md.String(), "report": d,
	})
}

// databaseAdoptionV1 answers the question a delivery report cannot: does anyone
// open it.
//
// A correct dashboard nobody looks at is worth nothing, and the audit trail has
// held the answer all along — who asked what, and which metrics nobody has ever
// asked for. Unused metrics are the finding: they are either modelled wrong or
// were never wanted, and both are worth knowing before the next engagement
// repeats them.
func (v *V1) databaseAdoptionV1(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, err)
		return
	}
	eng, _, ok := v.resolveGoverned(w, r)
	if !ok {
		return
	}
	id := orDefault(engine.DatabaseFromRequest(r), v.DBs.Default())
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 {
		days = d
	}
	ad, err := handover.Measure(r.Context(), eng, id, v.Engagement, days)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, ad)
}

// changedOutside reports every named part of the model that differs between two
// versions, other than `only`. Names appearing or disappearing count: an edit
// that drops a metric is exactly the failure this exists to catch.
//
// Comparison is on the parsed model, not the text, so reformatting, reordering
// and comment edits are free — which they should be, since the file is also
// hand-edited and carries the modeller's notes.
func changedOutside(before, after *semantic.Model, only string) []string {
	var out []string

	metrics := func(m *semantic.Model) map[string]semantic.Metric {
		byName := map[string]semantic.Metric{}
		for _, x := range m.Metrics {
			byName[x.Name] = x
		}
		return byName
	}
	dims := func(m *semantic.Model) map[string]semantic.Dimension {
		byName := map[string]semantic.Dimension{}
		for _, x := range m.Dimensions {
			byName[x.Name] = x
		}
		return byName
	}

	b, a := metrics(before), metrics(after)
	for name, x := range b {
		if name == only {
			continue
		}
		y, ok := a[name]
		if !ok {
			out = append(out, "metric "+name+" (removed)")
		} else if !reflect.DeepEqual(x, y) {
			out = append(out, "metric "+name)
		}
	}
	for name := range a {
		if name != only {
			if _, ok := b[name]; !ok {
				out = append(out, "metric "+name+" (added)")
			}
		}
	}

	bd, ad := dims(before), dims(after)
	for name, x := range bd {
		if name == only {
			continue
		}
		y, ok := ad[name]
		if !ok {
			out = append(out, "dimension "+name+" (removed)")
		} else if !reflect.DeepEqual(x, y) {
			out = append(out, "dimension "+name)
		}
	}
	for name := range ad {
		if name != only {
			if _, ok := bd[name]; !ok {
				out = append(out, "dimension "+name+" (added)")
			}
		}
	}

	// Entities and joins are the shape of the warehouse, not an opinion about
	// it. A scoped edit has no business touching them at all.
	if !reflect.DeepEqual(before.Entities, after.Entities) {
		out = append(out, "entities")
	}
	if !reflect.DeepEqual(before.Joins, after.Joins) {
		out = append(out, "joins")
	}

	sort.Strings(out)
	return out
}

// lintJSON renders lint issues as objects rather than the CLI's formatted lines,
// so a caller can group them by target or act on one.
func lintJSON(m *semantic.Model) []map[string]string {
	out := []map[string]string{}
	for _, is := range semantic.Lint(m) {
		out = append(out, map[string]string{
			"severity": is.Severity, "target": is.Target, "message": is.Message,
		})
	}
	return out
}
