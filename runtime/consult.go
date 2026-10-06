package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/liliang-cn/dataintelligence/agenttools"

	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/governance"
)

// The consulting loop over HTTP. Every route needs an identity; the decisions
// (adopt, reject) are the reason the loop has an HTTP surface at all — the
// copilot can record and propose, but only a person here can decide.
//
//	GET  /v1/whoami
//	GET  /v1/consult                       the whole engagement
//	POST /v1/consult/goals                 record a goal (baseline measured by the server)
//	GET  /v1/consult/goals/{id}
//	POST /v1/consult/findings              record a finding (evidence queries run by the server):
//	                                       {goal, says, evidence: [{asked, query: {metrics, group_by,
//	                                        filters, time: {from, to, last_days}, time_grain, …}}]}
//	POST /v1/consult/plans                 propose a plan
//	GET  /v1/consult/plans/{id}
//	POST /v1/consult/plans/{id}/adopt      {why, as_of?}  baseline → pin → actions
//	POST /v1/consult/plans/{id}/reject     {why}
//	POST /v1/consult/plans/{id}/measure    progress so far — not a verdict
//	POST /v1/consult/plans/{id}/accept     {as_of?}  the verdict, once the window closed
func (v *V1) mountConsult(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/whoami", v.whoamiV1)
	mux.HandleFunc("GET /v1/consult", v.consultRoute(v.consultBoard))
	mux.HandleFunc("POST /v1/consult/goals", v.consultRoute(v.consultAddGoal))
	mux.HandleFunc("GET /v1/consult/goals/{id}", v.consultRoute(v.consultGoal))
	mux.HandleFunc("POST /v1/consult/findings", v.consultRoute(v.consultAddFinding))
	mux.HandleFunc("POST /v1/consult/plans", v.consultRoute(v.consultPropose))
	mux.HandleFunc("GET /v1/consult/plans/{id}", v.consultRoute(v.consultPlan))
	mux.HandleFunc("POST /v1/consult/plans/{id}/adopt", v.consultRoute(v.consultAdopt))
	mux.HandleFunc("POST /v1/consult/plans/{id}/reject", v.consultRoute(v.consultReject))
	mux.HandleFunc("POST /v1/consult/plans/{id}/measure", v.consultRoute(v.consultMeasure))
	mux.HandleFunc("POST /v1/consult/plans/{id}/accept", v.consultRoute(v.consultAccept))
}

func (v *V1) whoamiV1(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeJSON(w, 401, map[string]any{"error": errText(err), "auth": v.AuthMode()})
		return
	}
	writeJSON(w, 200, map[string]any{"user": p.User, "role": p.Role, "auth": v.AuthMode()})
}

func errText(err error) string {
	if err == nil {
		return "unauthenticated"
	}
	return err.Error()
}

type consultHandler func(w http.ResponseWriter, r *http.Request, p governance.Principal)

func (v *V1) consultRoute(h consultHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := v.principalFrom(r)
		if !ok {
			writeErr(w, 401, errString(errText(err)))
			return
		}
		if v.Consult == nil {
			writeErr(w, 404, errString("the consulting loop is not enabled on this deployment (it needs a modelled default database)"))
			return
		}
		h(w, r, p)
	}
}

// consultErr maps a refusal to 422 with the rule that refused, a missing
// record to 404, and anything else (the warehouse, the store) to 502/500.
func consultErr(w http.ResponseWriter, err error) {
	var ref *consult.Refusal
	if errors.As(err, &ref) {
		code := 422
		if ref.Rule == consult.RuleNotFound {
			code = 404
		}
		writeJSON(w, code, map[string]any{"error": ref.Msg, "rule": ref.Rule})
		return
	}
	var gr *governance.Refused
	if errors.As(err, &gr) {
		writeErr(w, 403, err)
		return
	}
	writeErr(w, 502, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, err)
		return false
	}
	return true
}

func (v *V1) consultBoard(w http.ResponseWriter, r *http.Request, _ governance.Principal) {
	b, err := v.Consult.Board(r.Context())
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (v *V1) consultAddGoal(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var in consult.GoalInput
	if !decode(w, r, &in) {
		return
	}
	g, err := v.Consult.AddGoal(r.Context(), in, p, "")
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 201, g)
}

func (v *V1) consultGoal(w http.ResponseWriter, r *http.Request, _ governance.Principal) {
	g, err := v.Consult.GoalView(r.Context(), r.PathValue("id"))
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, g)
}

// findingBody takes evidence queries in the same shape the copilot's tools
// use ({metrics, group_by, filters, time, time_grain, …}).
type findingBody struct {
	Goal     string `json:"goal"`
	Says     string `json:"says"`
	Evidence []struct {
		Asked string         `json:"asked"`
		Query map[string]any `json:"query"`
	} `json:"evidence"`
}

func (v *V1) consultAddFinding(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var b findingBody
	if !decode(w, r, &b) {
		return
	}
	in := consult.FindingInput{Goal: b.Goal, Says: b.Says}
	for _, e := range b.Evidence {
		q, err := agenttools.ParseQuery(v.Consult.Model, e.Query, time.Now())
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		in.Evidence = append(in.Evidence, consult.EvidenceInput{Asked: e.Asked, Query: q})
	}
	f, err := v.Consult.AddFinding(r.Context(), in, p, "")
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 201, f)
}

func (v *V1) consultPropose(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var in consult.PlanInput
	if !decode(w, r, &in) {
		return
	}
	pl, err := v.Consult.ProposePlan(r.Context(), in, p, "")
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 201, pl)
}

func (v *V1) consultPlan(w http.ResponseWriter, r *http.Request, _ governance.Principal) {
	pv, err := v.Consult.PlanView(r.Context(), r.PathValue("id"))
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, pv)
}

type decisionBody struct {
	Why  string `json:"why"`
	AsOf string `json:"as_of"`
}

func (v *V1) consultAdopt(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	res, err := v.Consult.Adopt(r.Context(), r.PathValue("id"), p, b.Why, b.AsOf)
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (v *V1) consultReject(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := v.Consult.Reject(r.Context(), r.PathValue("id"), p, b.Why)
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (v *V1) consultMeasure(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	a, err := v.Consult.Measure(r.Context(), r.PathValue("id"), p)
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, a)
}

func (v *V1) consultAccept(w http.ResponseWriter, r *http.Request, p governance.Principal) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	a, err := v.Consult.Accept(r.Context(), r.PathValue("id"), p, b.AsOf)
	if err != nil {
		consultErr(w, err)
		return
	}
	writeJSON(w, 200, a)
}
