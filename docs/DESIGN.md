# DataIntelligence — AI-Native Data Platform — Design

**Working codename: DataIntelligence.** An AI-native data platform: ingest from anywhere → model & govern → transform with AI skills → flow through workflows (incl. human approval) → deliver anywhere — with rollback, lineage, and a natural-language agent that can both **query** and **act** on the data.

Date: 2026-06-25
Status: design (this doc = phase C). Build skeleton = phase B.

This synthesizes two inputs:
1. The **whiteboard product vision** (Build / Run / AI Agent; Sources → Data Model → Data Node → Data Flow → Tools/Skills → Destination; CDC/streams; execution dashboard + rollback; NL→dashboard).
2. The **course** *"Agent-Ready Data Platform: Semantic Layer, Text-to-SQL & MCP"* (the governed AI-query slice), already partly built as `semantic-go` + `semantic-mcp`.

> The course gave us the hardest, most valuable read-side core — a **governed, non-hallucinating AI query engine** (`semantic-go` compiler is built & verified on real Postgres). DataIntelligence wraps that core in a full **write + move + transform + flow + deliver** data platform.

---

## 1. Two planes

```
                         ┌──────────────────────────────────────────────┐
   INTELLIGENCE  ───────►│ AI AGENT: NL query · SQL/CRUD/transform gen · │
   (cross-cutting)       │ suggestions · NL→dashboard · exposed via MCP  │
                         └──────────────────────────────────────────────┘
                                          ▲
  DATA PLANE (Build)                      │ reads/acts through governed tools
  ┌─────────┐   ┌────────┐   ┌────────┐   ┌────────┐   ┌────────┐   ┌──────────────┐
  │ SOURCES │──►│ INGEST │──►│  DATA  │──►│  DATA  │──►│  DATA  │──►│ DESTINATIONS │
  │connectors│  │+mapping│   │ MODEL  │   │ NODES  │   │  FLOW  │   │   sinks      │
  │ CDC/strm│   │key/diff│   │canon + │   │field   │   │workflow│   │ ES/CRM/dash/ │
  │         │   │AI Diff │   │semantic│   │rules   │   │+human  │   │ snowflake/.. │
  └─────────┘   └────────┘   └────────┘   └────────┘   └────────┘   └──────────────┘
        │            │            │            │            │              │
  ┌─────┴────────────┴────────────┴────────────┴────────────┴──────────────┴─────┐
  │ CONTROL PLANE (Run): Execution Dashboard (proceed/reject · history · ROLLBACK)│
  │  · Data Explorer · Visualization · Data Lifecycle / lineage / chain-of-change │
  └──────────────────────────────────────────────────────────────────────────────┘
  ┌──────────────────────────────────────────────────────────────────────────────┐
  │ GOVERNANCE & OBSERVABILITY (cross-cutting): RBAC/RLS/masking/k-anon · identity │
  │  propagation · cost guardrails · audit · tracing · eval gates · drift detection│
  └──────────────────────────────────────────────────────────────────────────────┘
```

- **Data plane (Build)** moves and shapes data: Sources → Ingest → Model → Nodes → Flow → Destinations.
- **Control plane (Run)** operates it: execution dashboard with rollback, explorer, visualization, lineage.
- **AI Agent** is the intelligence layer over both: query in NL, generate transforms/SQL/CRUD, suggest mappings/rules, build dashboards from NL — all through governed tools (MCP).
- **Governance + Observability** wrap everything (lifted directly from the course).

---

## 2. Data plane — Build

### 2.1 Sources / Connectors
Pull or receive data; discover structure.
- **Batch/file**: CSV, Snowflake, DB tables.
- **Change/stream**: DB **CDC** (sequenced, rollback-aware), webhooks → realtime events, streams.
- **App/forms**: Web forms (Chrome extension / API), CRM (Hubspot/Salesforce/Zoho/Klaviyo).
- **Structure analysis**: read DDL → infer schema/API, ES schema, "event center".

```go
type Source interface {
    Discover(ctx) (SourceSchema, error)         // infer columns/types/keys
    Read(ctx, Cursor) (RecordBatch, Cursor, error) // batch or incremental
}
type ChangeSource interface {                    // CDC / streams
    Source
    Subscribe(ctx) (<-chan ChangeEvent, error)   // insert/update/delete, sequenced
}
```

### 2.2 Ingest & Mapping
Turn a source's shape into the platform's canonical Data Model.
- **Mapping**: `source field → model field` (the whiteboard "csv field → model field, name"). Authored or **AI-inferred** (suggest a mapping plan).
- **Key checks / data checks**: required keys present, types valid, dedupe keys.
- **AI Diff / model check**: detect when an incoming source drifts from the expected model (new/renamed/retyped columns) — the ingest-time analogue of the course's drift detection.

```go
type MappingPlan struct {
    Source string
    Fields []FieldMap   // {SourceField, ModelField, Transform, Required}
    Keys   []string
}
type Ingestor interface {
    InferMapping(ctx, SourceSchema, *Model) (MappingPlan, []Suggestion, error) // AI-assisted
    Ingest(ctx, RecordBatch, MappingPlan) (IngestResult, error)                // applies key/data checks
    Diff(ctx, SourceSchema, MappingPlan) (ModelDiff, error)                    // AI Diff
}
```
(Note: `cortexdb`'s `importflow` already does CSV/SQL-dump → RAG+KG with AI-assisted mapping — reuse/extend it here.)

### 2.3 Data Model (two facets)
- **Canonical model** (storage/ingest): entities, fields, types, keys — what data *is*. The mapping target.
- **Semantic model** (query/analytics): metrics, dimensions, join graph — what data *means*. **This is `semantic-go`** (built & verified). The two share entity definitions; the semantic facet adds metrics/joins for the AI query layer.

### 2.4 Data Node — field-level rule engine
A node is a governed validation/transform point on fields/records.
- **Update rules**: conflict resolution, required, update policy; **source-based** (trust source X over Y) and **value-based** (pick max/latest/non-null).
- **Field-level reporting / alert rules**: flag/notify when a field violates a rule.

```go
type FieldRule struct {
    Field    string
    Kind     string // conflict | require | update | alert
    Strategy string // source_based | value_based | latest | max | nonnull ...
    Params   map[string]any
}
type Node interface {
    Apply(ctx, Record) (Record, []RuleEvent, error) // returns transformed record + rule events
}
```

### 2.5 Data Flow — workflow engine
A DAG of nodes/skills with control flow and **human-in-the-loop**.
- **Triggers**: on-upload, on-click, scheduled (cron), event/webhook.
- **Node types**: transform nodes, skill nodes, **Human Node** (approval gate), merge/fork.
- **Rollback**: every run is versioned; a run (or step) can be rolled back ("Merge fork / roll back", "chain of change").
- **Runtime**: scheduler, queue, retries, "night watch".

```go
type Flow struct {
    Trigger Trigger          // upload | click | cron | event
    Steps   []Step           // node/skill/human, with edges
}
type Step struct {
    ID   string
    Type string             // node | skill | human | merge | fork
    Ref  string             // node/skill name
    Next []string           // edges (DAG)
}
type FlowEngine interface {
    Run(ctx, Flow, Input) (RunID, error)
    Approve(ctx, RunID, StepID, by Principal) error // human node
    Rollback(ctx, RunID) error                      // chain-of-change reversal
    Status(ctx, RunID) (RunStatus, error)
}
```
(`agent-go` provides Tasks/TaskPlan + checkpoints — a strong base for the flow/agent execution + rollback checkpoints.)

### 2.6 Tools / Skills
Reusable capabilities a node/flow/agent invokes — AI and deterministic.
- **Transform** (CSV→model, STD model, i18n, templating), **Enrich** (address/location), **Match/dedupe**, **Extract relational**, **SQL/CRUD gen**, **Error detection**.
- Skills are `agent-go` skills/tools (uniform contract, callable by the agent or pinned in a flow).

### 2.7 Destinations / Sinks
Deliver results.
- **Search** (ES), **Alert/Notification**, **AI Context** (RAG/knowledge store — `cortexdb`), **CRM**, **Business apps**, **Data Explorer**, **Dashboards**, **Snowflake**, **reports**.

```go
type Sink interface {
    Write(ctx, RecordBatch) (WriteResult, error)
    Capabilities() SinkCaps // upsert? streaming? schema?
}
```

---

## 3. Control plane — Run

- **Execution Dashboard**: per-run timeline; **proceed / reject** at human nodes; **history + rollback**; status/errors/cost.
- **Data Explorer**: browse records, schemas, and **lineage** (where a field came from, what transformed it).
- **Visualization / Dashboards**: charts; **NL→Dashboard generation** (agent builds a dashboard spec from a sentence).
- **Data Lifecycle / chain-of-change**: end-to-end provenance — every record/field carries its origin + transform history; powers rollback and audit.

---

## 4. AI Agent layer (the intelligence)

The agent uses the platform through **governed MCP tools** — never raw access.

- **NL query** = the course's grounded text-to-SQL over the **semantic model** → **this is `semantic-mcp`** (semantic-go compiler + grounding + governance + MCP tools `list_metrics`/`get_dimensions`/`query_metric`). Read side, done right.
- **Generation**: SQL / CRUD / **transform** generation (propose a node/mapping/flow step), validated before run.
- **Suggestions**: recommend mappings (ingest), field rules (nodes), fixes (errors), metrics (model).
- **NL→Dashboard**: compose a visualization spec from a question.
- **Act safely**: any *write/transform* the agent proposes goes through the flow engine's **propose→approve→commit** + audit (course module 19) — the agent opens a PR, a human merges.

Built on `agent-go` (LLM via `llm.NewOpenAI`, agent loop, structured output, MCP, skills) + `cortexdb` (retrieval for metric/mapping/skill selection).

---

## 5. Cross-cutting — Governance & Observability (from the course)

Applies to BOTH planes (query AND pipeline):
- **Policy in the layer, not the prompt**: RBAC (who may resolve a metric / run a flow), RLS (row scope), column masking, k-anonymity — enforced structurally, plus warehouse-level belt-and-suspenders.
- **Identity propagation** (keystone): the real human reaches the warehouse/sink, never a shared service account; MCP token broker (RFC 8693), audience binding (RFC 8707).
- **Guardrails**: cost/time/result ceilings; read-only by default; write approvals.
- **Audit + lineage**: append-only record of who/what/why for every query AND every pipeline step; chain-of-change.
- **Eval + validation**: `eval-go` gates — accuracy of NL answers AND data-validation checks on ingest/transform; nightly drift detection (schema + semantic).
- **Observability**: one `trace_id` across agent→tool→semantic→SQL→warehouse and source→ingest→node→flow→sink; cost/latency per span.

---

## 6. Worked flows

**A. Ingest (whiteboard "NY CSV"):** upload CSV → `Discover` schema → agent `InferMapping` (csv field→model field) + Suggestions → human approves mapping (Human Node) → `Ingest` with key/data checks → `AI Diff` vs model → land into canonical model (DB / STD model) → trigger downstream Flow → Destinations. Every step logged; run rollback-able.

**B. NL query (course):** "net revenue by region last quarter" → agent retrieves relevant metrics (cortexdb) → emits a **semantic query** → `semantic-go` compiles fanout/chasm-safe SQL → governed execution (RLS/masking/cost) → answer with citations/receipt → optionally NL→Dashboard. *(This path is built & verified.)*

---

## 7. Mapping to the Go ecosystem

| DataIntelligence module | Reuses | New work |
|---|---|---|
| Semantic model + compiler | **semantic-go** v? (built, verified) | — |
| NL query + governance + MCP (read core) | **semantic-mcp** (in progress) | finish grounding + MCP |
| AI Agent (LLM, loop, skills, gen, MCP) | **agent-go** v2.92.0 | platform-specific skills |
| Retrieval (mappings/metrics/skills) + AI Context sink + KG lineage | **cortexdb** v2.33.0 (+ `importflow` for ingest) | wire lineage graph |
| Eval gates + data validation | **eval-go** v0.1.0 | validation metrics |
| Governance patterns (RBAC/mask/audit) | **compliant-rag** | flow-level policy |
| Warehouse/sink SQL | semantic-go `Dialect` | connectors |
| **Sources/Connectors, CDC/streams** | — | **new** |
| **Ingest/Mapping/AI-Diff** | cortexdb importflow (partial) | **extend** |
| **Data Node rule engine** | — | **new** |
| **Data Flow / workflow + human + rollback** | agent-go Tasks/checkpoints (base) | **new engine** |
| **Destination connectors** | — | **new** |
| **Execution dashboard / explorer / viz** | — | **new (UI later)** |

---

## 8. Repo / module layout (for phase B)

```
ei/
  docs/DESIGN.md            (this)
  connectors/               Source/ChangeSource/Sink interfaces + impls (csv, postgres-cdc, webhook, snowflake, crm, es)
  ingest/                   mapping plan, key/data checks, AI Diff (reuse cortexdb importflow)
  model/                    canonical data model; bridges to semantic-go semantic model
  nodes/                    field-level rule engine
  flow/                     workflow DAG engine: triggers, human nodes, scheduler/queue, rollback
  skills/                   transform/enrich/match/gen (agent-go skills)
  agent/                    NL query (semantic-mcp), gen, suggestions, NL→dashboard
  governance/               RBAC/RLS/masking/k-anon/audit (compliant-rag patterns)
  obs/                      tracing, cost/latency, lineage/chain-of-change
  runtime/                  execution dashboard API, data explorer API
  eval/                     eval-go gates + data validation
  server/                   API + MCP server
  cmd/ei/                   CLI
  deploy/                   docker-compose (Postgres warehouse + platform)
```
External deps stay as published modules: semantic-go, agent-go, cortexdb, eval-go.

---

## 9. Build order (phase B roadmap)

1. **Spine first (reuse the verified core):** wire `semantic-go` + `semantic-mcp` as the `model` + `agent`(query) modules → end-to-end NL→answer on the live Meridian warehouse.
2. **Connectors + Ingest:** CSV + Postgres source; mapping (AI-inferred) + key/data checks + AI Diff (extend cortexdb importflow). Land the "NY CSV" flow.
3. **Data Node rule engine:** field-level conflict/require/update + alert rules.
4. **Flow engine:** DAG + triggers (upload/cron/event) + **Human Node** + scheduler/queue + **rollback / chain-of-change**.
5. **Destinations:** ES + Dashboard + AI Context (cortexdb) + Snowflake; Alert.
6. **Runtime:** execution dashboard API (proceed/reject/rollback/history) + data explorer + lineage.
7. **AI Agent breadth:** transform/CRUD gen, suggestions, NL→dashboard.
8. **Cross-cutting hardening:** governance everywhere, eval + validation gates, observability/lineage, drift.

**Milestone per slice = a runnable end-to-end demo** (e.g. slice 1 = "ask Meridian in English"; slice 2 = "drop a CSV, AI maps it, human approves, it lands").

---

## 10. Open decisions (resolve at start of B)

- **Codename / module path** (keep `DataIntelligence`? final name → `github.com/liliang-cn/<name>`).
- **One monorepo (`ei/`) vs multi-module** — recommend a single `ei` module that imports the published libs (semantic-go/agent-go/cortexdb/eval-go).
- **Flow engine: build vs lean on agent-go Tasks/TaskPlan** — recommend building a thin DAG/flow layer that uses agent-go for the agent/skill steps and its checkpoints for rollback.
- **UI** (execution dashboard / explorer / viz) — API-first now; web UI later (out of scope for early B).
- **Warehouse vs operational store** — Postgres serves both for now (analytics + canonical model); split later if needed.

---

**Through-line:** DataIntelligence is an AI-native data platform where the **governed semantic/query core (already built) is one module**, and the new work is the **data plane (connect → ingest/map → node rules → flow → deliver)** plus the **runtime (rollback/lineage)** — all under the course's governance/observability discipline, with an agent that can both *ask* and *act* through governed tools.

---

## 16. Status & remaining work (honest, vs this design's full depth)

The §9 build order is **scaffolded end-to-end (prototype depth)** and verified with runnable commands. This section tracks each design area against its **full depth** — ✅ done · 🟡 partial · ❌ not built.

| Design area | Status | What's done / what's missing |
|---|---|---|
| §4 Semantic layer & compiler | ✅ | entities/dims/metrics(simple/ratio/derived), join graph, aggregate-then-join, refusal, DimensionsFor |
| §4 Metric depth | 🟡 | **time intelligence done** in core compiler: rolling / cumulative / delta / prior window metrics (`of` + `window`). missing: grain-to-date period reset, bridge (m:n), role-playing dims, additivity enforcement, metadata review gate |
| §5 Grounding / Text-to-SQL | ✅ | NL→semantic query + full **context engineering**: **hybrid retrieval** (cortexdb BM25 ⊕ dense embeddings — DashScope `text-embedding-v4`, in-process cosine fusion 0.4/0.6) → **few-shot exemplar bank** (`models/exemplars.yaml`, embedding-retrieved, shape-deduped, injected into the prompt; `di exemplar` promotes fixed misses) → LLM over pruned set → clarify gate. **NL accuracy 12/12** via `di nleval` with LLM. missing: cross-encoder rerank wiring, cost benchmark |
| §6 Evaluation | ✅ | reconciliation/drift gate (`di eval`) **+ NL eval closed-loop** (`di nleval`): labeled set (`models/nl_evalset.yaml`), 3-axis scoring (semantic / execution / **result-match vs control SQL**), governance probes (refused-is-pass), metric-confusion matrix, per-category CI gate (`-min`/`-floor`, exits 1 on regression), accuracy dashboard (`_nl_eval_runs`/`_nl_eval_cases`, `GET /nleval`), and an **eval-go LLM-judge groundedness layer** (faithfulness + answer-relevancy when LLM creds set). Verified 11/11 offline. missing: cost/latency-per-question panel |
| §7 Governance | 🟡 | RBAC + result masking + **RLS (attr-bound, fail-closed)** + **k-anonymity** + audit in `governance.Query`, **plus warehouse-level RLS** (belt-and-suspenders: `SET LOCAL ROLE` + Postgres row-security policy bound to the on-behalf-of session — the engine enforces even if the app layer misses; `di obo`). missing: threat-model-as-code |
| §8 Guardrails | 🟡 | timeout + row cap + **production write-back** (`writeback/`): typed allowlisted proposals, propose→approve→commit, separation of duties, max-affected cap, red-line wall, atomic rollback, audit + **read-only-by-default** (`di_app` least-priv role for governed reads). missing: spend/credit caps |
| §9 MCP server | ✅ | official SDK, 3 thick tools + ingest, no run_sql |
| §9 MCP security | ✅ | bearer identity → principal, per-tool scopes, rate limit + **real OIDC/JWT verifier** (`mcp/oidc.go`): RS256 via JWKS or static key, issuer/audience/expiry/nbf validation (audience = confused-deputy cure, RFC 8707; rejections → 401), claims→principal + **RFC 8693 on-behalf-of token exchange** (`mcp/obo.go`) + **per-user DB session** (`warehouse.QueryAs`: `SET LOCAL ROLE` least-priv + `app.*` GUCs) so identity reaches the warehouse and **Postgres RLS enforces** (`di obo setup/demo/chain`). `di token` dev issuer. Adversarial + live tests + no-regression. missing: automated pen-test gate (lesson 75) |
| §10 Agent loop | ✅ | agent-go agent (`di agent`) + **plan-query-critique loop** (`critic/`, `di ask -critic`): rule critic (coverage/sanity/metric-identity) ⊕ LLM critic (grain/identity) → pass\|revise\|ask_user, feedback-fed re-grounding, bounded retries + cycle guard + graceful degradation (lessons 77/80) · **conversation memory + multi-metric chaining** (`convo/`, `di chat`/`di chain`): typed cross-turn state with field-level merge incl. WHERE filters / topic-shift reset (lesson 78), multi-step plans with dependency edges + sub-result caching (lesson 79). all verified live. (left: conversational write-back — see §-Agent patterns) |
| §11 Observability | 🟡 | per-request trace (trace_id + compile/execute spans + latency + attrs) in `_traces`, served via `/traces`; + `_audit`. missing: full OTel SDK, cross-process propagation, bytes/credits cost |
| §12 Production hardening | 🟡 | drift gate + **stable-SQL cache** + **graceful degradation** (stale-on-error) + **shadow rollout** (`di shadow`) + **runbook**. missing: canary traffic-split, lineage cache invalidation, version registry |
| Connectors/Sinks | ✅ | **diverse, manifest-driven sources** (`connectors/`, `di source`, `examples/meridian/sources.yaml`): CSV · Postgres-CDC (`di cdc`) · **MySQL · MongoDB · Redis · S3/MinIO · Kafka/Redpanda** (all real, Dockerized, verified live) · **Twenty CRM** (real REST sync `di crm` + **webhook push** receiver `di webhook` via cloudflare tunnel). Platform stays neutral — generic adapters keyed by `type`+config; the manifest supplies the domain. sinks: log/json/table/alert + real **Elasticsearch** `_bulk` + Snowflake (dry-run). missing: live Snowflake driver |

### Remaining plan (priority order)
1. ~~Context engineering (§5 / M9)~~ — **DONE**: cortexdb top-K retrieval + clarify gate (`grounding/`). (left: few-shot bank, embedding retrieval)
2. ~~MCP security (M17)~~ — **DONE**: bearer identity → governance principal, scopes, rate limit, `di mcp -http`. (left: real OIDC, on-behalf-of, tenant isolation)
3. ~~Agent loop (M18)~~ — **DONE** via agent-go (`di agent`) over the governed MCP server. (left: formal critic, retry guards, cross-turn memory)
4. ~~Governance depth (M12-13)~~ — **DONE**: RLS (attr-bound) + k-anonymity + audit in `governance.Query`. (left: warehouse-level policies, spend caps, read-only grants)
5. ~~Time intelligence (M4)~~ — **DONE**: rolling/cumulative/delta/prior window metrics in the core compiler. (left: grain-to-date reset)
6. ~~Observability (M20)~~ — **DONE**: trace (trace_id + spans + latency) in `_traces`, `/traces` API + receipt. (left: full OTel, cost/bytes)
7. ~~Hardening (M21)~~ — **DONE**: stable-SQL cache + graceful degradation + `di shadow` + runbook. (left: canary split, version registry)
8. ~~Connectors/Sinks breadth~~ — **DONE + extended**: diverse manifest-driven sources (CSV · Postgres-CDC · MySQL · MongoDB · Redis · S3/MinIO · Kafka/Redpanda · Twenty CRM REST + webhook) all real & verified live; ES `_bulk` sink. Platform stays neutral — generic adapters keyed by type+config, the example manifest supplies the domain. (left: live Snowflake driver)

**All 8 remaining-plan items complete.** Further work = depth within each (warehouse-level governance policies, full OTel, canary traffic-split, live Snowflake/CRM, webhook source).

**Follow-up — Config-driven DataFlow saga (whiteboard "Data Flow"), neutral: DONE.** The flow engine (`flow/`) exposes four generic step PRIMITIVES — `ingest` (a manifest source → table), `sql` (do/undo), `mutate` (snapshot → apply → restore on rollback), `human` (approval) — and a `LoadDir` that compiles a YAML flow file into steps. **No domain flow logic lives in the platform binary**; flows are data under `examples/meridian/flows/*.yaml`. Verified live: `supplier-price-update` (S3 catalog → stage → margin impact → human approve → apply costs → recompute margins) committed real cost changes, then `di flow rollback` **restored every cost exactly** and dropped the derived tables via reverse compensation; `contacts-ingest` runs the same way. Earlier these flows were Go funcs in `cmd/di` (business code in the platform) — that was refactored out into config.

**Follow-up — Neutral multi-source layer (whiteboard "Sources"): DONE.** The platform is **domain-neutral** — Meridian Retail is one *example integration*, never hardcoded. `connectors/` ships generic adapters by TYPE (mysql · mongo · redis · s3 · kafka · csv · postgres-cdc · crm); a `Manifest` (`examples/meridian/sources.yaml`, env-expanded for secrets) supplies the domain. `di source list|read|ingest` runs any source and stages it into the warehouse. Verified live against 5 real Dockerized systems (MySQL online orders · MongoDB reviews · Redis live inventory · MinIO supplier catalog · Redpanda order events), all tied to Meridian and cross-joined (e.g. a customer's in-store Postgres orders ⋈ online MySQL orders on email). Adding a domain never touches platform code; adding a source *type* is the only code change.

**Follow-up — Module 10 NL evaluation closed-loop: DONE.** `di nleval` + `nleval/` package: labeled set, semantic/execution/result scoring against control SQL, governance probes, metric-confusion, per-category CI gate (exits 1 on regression), accuracy dashboard (`/nleval`), eval-go judge layer. 11/11 offline. This moves **§6 Evaluation → ✅** and supplies §5's NL accuracy batch. (left: cost/latency-per-question panel)

**Follow-up — Diverse enterprise source landscape (whiteboard "Sources" node, neutral platform): DONE.** The platform is a generic, manifest-driven connector engine; **Meridian is one example integration, never hardcoded** (see [memory: neutral platform]). Generic adapters: `connectors/{mysql,mongo,redis,s3,kafka,manifest}.go`; `di source list|read|ingest` reads `examples/meridian/sources.yaml`. Stood up + seeded 5 real systems (Docker, `deploy/sources/`): **MySQL** online orders (50, real customer emails), **MongoDB** product reviews (24), **Redis** live inventory (192 stock hashes), **S3/MinIO** supplier price-list CSV, **Redpanda/Kafka** order events (25). Verified live through the connectors and **unified in the warehouse** (online orders ⋈ PG customers by email; supplier feed ⋈ products). Secrets via `${ENV}` in the manifest. Manifest unit test + live verification.

**Follow-up — Whiteboard "AI Agent: CRUD / transform gen" (production write-back): DONE → §8 ✅ depth.** `writeback/` + `di propose/proposals/approve/reject/revert`:
- *NL → typed proposal* (`generate.go`, agent-go): emits structured JSON (op/table/set/where, or a metric YAML) — never SQL — confined to `models/writeback.yaml` (the writable allowlist: tables, columns, ops, enums, required fields, max-affected, require-predicate).
- *Validate + dry-run* (`proposal.go`/`plan.go`): allowlist + type/enum + mandatory-predicate checks; parameterized SQL builder; preview shows the **before-image** and **affected-row count**; over-cap is refused.
- *Govern + commit* (`engine.go`): role-gated propose vs approve, **separation of duties** (proposer ≠ approver), atomic apply in one tx with a consistent before-image snapshot, full audit (`_proposals`/`_writeback_audit`).
- *Rollback*: inverse ops from the before-image (insert→delete by PK, update→restore, delete→re-insert).
- *Transform gen*: NL → a metric/dimension merged into a **candidate model that must `semantic.Load` + `Compile`** before it's ready; commit backs up the model YAML and writes the merge.
- Verified live: UPDATE order status (commit→DB changed→revert→restored), INSERT refund (+revert), generated `avg_refund_per_order` metric (compiles & runs); guardrails refused over-cap + same-principal approval. Unit tests cover validation / SQL builder / red-line / roles.

**Follow-up — Module 17 securing MCP (lessons 72/73/74): DONE → §9 ✅.**
- *Real OIDC/JWT verifier* (`mcp/oidc.go`, no third-party dep): RS256 via **JWKS** (fetched+cached) or static key; validates **issuer / audience / exp / nbf** (audience = confused-deputy cure, RFC 8707); failures wrap `auth.ErrInvalidToken` so `RequireBearerToken` answers **401, not 500**; claims → principal. `di token gen-key|mint` dev issuer; `di mcp -oidc`. Live: wrong-aud / expired / tampered / unknown-kid → 401; valid passes.
- *RFC 8693 on-behalf-of* (`mcp.ExchangeToken`): the validated caller (subject) token is exchanged for a fresh token **re-scoped to the warehouse audience** with an `act` (actor) claim — the client token is never forwarded. Test asserts the exchanged token is valid at the warehouse audience and rejected at the MCP-server audience.
- *Per-user DB session* (`warehouse.QueryAs`): opens a read-only tx, `SET LOCAL ROLE di_app` (least-priv — superusers bypass RLS), and sets `app.user/role/tenant/region` via `set_config(...,true)`. A **Postgres RLS policy** (`deploy/schema/03_obo_rls.sql`, `di obo setup`) on `stores` reads `current_setting('app.region')`, so the **warehouse itself** scopes a manager to their region. `governance.Query` runs through this when `DI_DB_APP_ROLE` is set (belt-and-suspenders). Live (`di obo demo/chain`): same SQL, manager session → only South, admin → all 4; full chain verify→exchange→DB verified; nleval **11/11 no regression** (unscoped roles still see all). (left: automated pen-test gate, lesson 75.)

**Follow-up — Module 18 agentic analytics: DONE (lessons 77/78/79/80) → §10 Agent loop ✅.**
- *Plan-query-critique* (`critic/`, `di ask -critic`): `Chain` runs deterministic `RuleCritic` (coverage/sanity/metric-identity) then `LLMCritic` (grain/identity) only if rules pass; verdict pass|revise|ask_user; revise folds the reason back into `GroundWithFeedback` and retries, bounded by `MaxRetries` with a **cycle guard** (repeated plan → ask_user) + graceful degradation. Live: clean Q passes in 1 attempt; ambiguous "total revenue…on average" oscillates → cycle guard → clarify.
- *Typed cross-turn memory* (`convo/Session`, `di chat`): a small typed query state (not the transcript); each turn field-level **merges** onto the prior (incl. WHERE filters — "just the South" added `region='South'`) or **resets** on a topic shift; sub-result cache per thread. Grounding now emits `where` so filters are expressible.
- *Multi-metric chaining* (`convo/RunChain`, `di chain`): LLM decomposes into ordered steps with `${stepID}` dependency edges; each metric a separate governed call; a `pick` (top/bottom by metric) feeds the next step's filter. Live: "refunds for the highest-revenue region" → s1 picks West → s2 refund_total WHERE region=West.

**Follow-up — Module 9 context engineering (embedding + few-shot): DONE.** Hybrid retrieval (BM25 ⊕ dense `text-embedding-v4`, in-process cosine fusion) + few-shot exemplar bank (`models/exemplars.yaml`, embedding-retrieved, shape-deduped, prompt-injected; `di exemplar` promotes fixed misses). Creds from `DI_EMBED_*` env (never in code). Verified: **NL eval 12/12 with LLM**, incl. the window-metric and ambiguity-clarify cases. This moves **§5 Grounding → ✅**. (left: cross-encoder rerank wiring, cost benchmark) · cortexdb bumped to v2.35.0.

"Done" earlier meant "§9 slices scaffolded", not "full course depth". This table is the source of truth for what's left.
