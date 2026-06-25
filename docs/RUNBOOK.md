# DataIntelligence — Operations Runbook

A pilot's checklist for on-call. Lives in the repo next to the code; reviewed after every incident.

## Paging SLOs (page only on these; everything else is a dashboard)

| SLO | Threshold | Signal |
|---|---|---|
| **Accuracy** | eval pass-rate < 100% of `di eval` | reconciliation/drift gate fails |
| **Cost / latency** | trace `total_ms` P95 over target, or credits/hr over budget | `/traces`, warehouse history |
| **Availability** | `/healthz` failing or query error-rate up | API health, `_audit` errors |
| **Governance** | any RBAC/RLS/mask bypass | pen-test gate, `_audit` `refused` anomalies |

## Incident flow: Detect → Triage → Mitigate → Resolve

1. **Detect** — an SLO breach pages on-call with the failing artifact (eval case, trace_id, audit row).
2. **Triage** — open the trace (`GET /traces/{id}`): which span is wrong? Localize to a layer:
   - rows wrong → **semantic model** (metric/dimension/join)
   - total low / truncated → **result cap** → aggregate in the layer
   - wrong sentence, right rows → **agent/formatting**
   - slow but correct → **cache** (warm it), not the model
   - refused unexpectedly → **governance** (RBAC/RLS/scope) or a missing join edge
3. **Mitigate (stop the bleeding first)** — a single config change, before root-cause:
   - bad answers after a model change → **re-pin the model version** (`-model models/meridian.<lastgreen>.yaml`); the v1 cache is warm, users recover in seconds.
   - cost spike → tighten warehouse `Timeout`/`MaxRows`; quarantine the offending agent (revoke its scope/token).
   - warehouse down → the cache serves **labeled stale** answers; if cold, the platform returns an honest "can't answer", never an invented number.
4. **Resolve** — fix upstream **in the model** (never the prompt), add a regression case to `di eval`, re-promote via shadow → canary.

## Safe rollout (model versioning)

- The semantic model is a versioned file; agents/queries pin a version via `-model`.
- **Shadow:** `di shadow -a models/meridian.yaml -b models/meridian.next.yaml -metrics ... -by ...` compiles+runs a query through both and diffs the result — run before promoting.
- **Canary:** route a fraction of traffic to the new model; gate on `di eval`; a failing gate halts the rollout. **Rollback = re-pin to last-green** (fast; cache warm).

## Drift

- `di eval` is the reconciliation gate; run it **nightly against production**. A silent schema change (column rename/retype, grain/join shift) compiles but fails the gate → alert.

## Where to look

- `GET /traces`, `/traces/{id}` — per-request spans + latency
- `_audit` table — who/what/SQL/refused per query
- `_flow_runs` — pipeline runs + rollback history (chain-of-change)
- `GET /cache` — hit/miss; `GET /lineage?table=` — what touched a table

> Test: a brand-new on-call engineer should resolve a drift page using only this card. Rehearse a rollback quarterly.
