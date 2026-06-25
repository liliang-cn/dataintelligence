# semantic-mcp — Design Document

**An agent-ready data platform: semantic layer + grounded Text-to-SQL + governed MCP server, in Go.**

Date: 2026-06-25
Status: foundation built (warehouse + connector + semantic model types); layers being implemented in dependency order.

This document is the synthesis of the full course *"Agent-Ready Data Platform: Semantic Layer, Text-to-SQL & MCP"* (97 lessons, 23 modules), mapped onto a concrete Go implementation that reuses the existing ecosystem: **agent-go** (LLM + agent loop + MCP), **cortexdb** (metric/example retrieval + the retrieval-layer `Authorize` gate), **eval-go** (accuracy regression gates).

---

## 1. The problem (why this exists)

"Just let people ask the warehouse in English" fails not because models are dumb, but because **meaning is missing**. The same word — *revenue* — resolves three different ways across finance (net), marketing (gross), ops (shipped). It lives in ~40 dashboards, ~12 transform models, and people's heads. Every consumer re-derives it with slightly different SQL, and they drift.

A naive LLM makes this **worse, not better**: it picks one wrong answer with total confidence, runs clean, returns a green check. Public benchmarks put raw Text-to-SQL at ~40% execution accuracy on real schemas.

### The 5-hop request (each hop silently changes the answer)
1. **Intent** — agent misreads "revenue" / "last quarter".
2. **Tool choice** — calls raw SQL when a governed metric existed.
3. **SQL generation** — invents a fan-out join.
4. **Execution** — runs with over-broad permissions.
5. **Return** — wrong number with a confident green check.

### The 5 failure modes the platform must structurally prevent
1. **Wrong join** — relates wrong keys/tables (invented from name similarity).
2. **Wrong grain** — aggregates at order level when line level was meant.
3. **Fan-out** — a one-to-many join multiplies a measure and inflates totals.
4. **Ambiguous metric** — "revenue" has 3 definitions; picks one blindly.
5. **Silent wrong answer** — runs clean, no error, simply incorrect. *A crash is a gift; a silent wrong answer is the real enemy.* **Valid SQL ≠ correct SQL.**

### The maturity ladder (the platform's spine)
L0 raw schema (joins guessed, ~40%) → L1 documented schema (still guessed) → L2 **semantic layer** (grounds metrics/dims/joins, ~90%) → L3 **governance travels with the layer** → L4 **MCP** as the governed, observable contract.

---

## 2. Architecture

```
  User (Slack / web / CLI)
        │  natural language
        ▼
  ┌─────────────────────────────────────────────────────────────┐
  │ AGENT LOOP        decompose → plan → query → critique → answer│  agent-go
  └─────────────────────────────────────────────────────────────┘
        │  semantic query (intent: metric × dimensions × filters × grain)
        ▼
  ┌─────────────────────────────────────────────────────────────┐
  │ GROUNDING / TEXT-TO-SQL   NL → retrieve relevant metrics      │  agent-go LLM
  │   (cortexdb), few-shot exemplars, disambiguate, emit intent   │  + cortexdb
  └─────────────────────────────────────────────────────────────┘
        │  validated semantic query (never raw SQL)
        ▼
  ┌─────────────────────────────────────────────────────────────┐
  │ SEMANTIC LAYER   model (entities/dimensions/metrics/joins)    │  semantic/
  │   COMPILER: aggregate-to-grain-in-CTE → join → dialect SQL    │
  └─────────────────────────────────────────────────────────────┘
        │  fanout/chasm-safe SQL (+ partition/date filters)
        ▼
  ┌─────────────────────────────────────────────────────────────┐
  │ WAREHOUSE CONNECTOR   database/sql + Dialect + cost guardrails│  warehouse/
  └─────────────────────────────────────────────────────────────┘
        ▼
     Real warehouse (Postgres today; Snowflake/Databricks/DuckDB = dialects)

  ┌─ GOVERNANCE BAND (every hop) ─────────────────────────────────┐
  │ RBAC · row-level security · column masking · k-anonymity ·    │
  │ cost ceilings · read-only · approvals · audit trail          │
  └──────────────────────────────────────────────────────────────┘
  ┌─ OBSERVABILITY (every hop) ───────────────────────────────────┐
  │ one trace_id across question→agent→tool→semantic→SQL→rows;   │
  │ cost/latency per span; eval gates on every change            │
  └──────────────────────────────────────────────────────────────┘

  Exposed to agents via:  MCP SERVER (thick tools) — mcp/
```

**Build order is load-bearing — meaning first, transport last.** Skip a layer and the next inherits an unpatchable hole.

---

## 3. Data model — Meridian Retail (normalized, already built)

Deliberately **not** a single pre-aggregated fact, so the join graph reproduces the real traps the course teaches.

| Table | Grain | Role |
|---|---|---|
| `suppliers` | supplier | dim; reached only via products (missing-edge demo) |
| `products` | product | dim (category/brand) + `supplier_id`, `unit_cost` |
| `stores` | store | dim; `region` = row-level-security key |
| `customers` | customer | dim; `email` = PII (masking demo) |
| `orders` | one order | fact header: date/store/customer/status |
| `order_items` | one line | fact: `quantity`, `unit_price` (additive only at this grain) |
| `refunds` | one refund | second 1:many child of orders (chasm with order_items) |

Reproduces: **fanout** (orders 1:many order_items), **chasm** (orders has two 1:many children), **missing edge** (order_items→suppliers only via products), **PII masking** (customers.email), **row security** (stores.region).

Seed is deterministic (`setseed`) → stable known-good numbers for eval. Lives in `deploy/` (docker-compose Postgres on host port **39632**, schema + seed auto-applied). Verified known-good: **total_revenue = 6,178,743.62** over 12,000 line items in 5,000 orders.

---

## 4. Layer 1 — Semantic layer (the heart)

### 4.1 The three nouns (typed → invalid combos rejected, not hallucinated)
- **Entity** — a real business thing with a declared **primary key** the layer joins on (never guessed). `order`, `order_item`, `product`, `store`, `customer`, `supplier`.
- **Dimension** — an attribute to group/filter by, named in business words and **typed** (`categorical` | `time`). Carries an optional `mask` SQL expression (governance lives in the dimension definition).
- **Metric** — an aggregated number with **grain + aggregation locked in**, so totals never double count. Types:
  - `simple` — `agg` (sum/count/count_distinct/avg/last_value) over `expr` on a base entity.
  - `ratio` — numerator/denominator metrics, each computed at its own grain, then divided (e.g. AOV = revenue / order_count). Never store a ratio.
  - `derived` — expression over other metrics (e.g. `net_revenue = total_revenue - refund_total`); how chasm traps are avoided (each base metric aggregates in its own CTE).
  - `cumulative` — rolling/grain-to-date windows.
- **Additivity** per measure: additive / semi-additive (`last_value` over time) / non-additive (ratios, distinct counts) — the compiler refuses illegal aggregations.

Query shape: **`<metrics> by <dimensions> [filtered by <dimensions>] [at <time grain>]`**. Zero table names, zero join keywords from the caller.

### 4.2 The join graph (the single highest-leverage fix)
Nodes = entities, edges = declared relationships carrying **keys + cardinality** (`many_to_one` | `one_to_many` | `many_to_many`). The compiler **only traverses declared edges; it cannot invent a join.** A missing edge → explicit error, never a guess. *"A refused join is a feature."* Many-to-many goes through a **bridge** (two many-to-one edges); role-playing dimensions (order_date vs ship_date) are distinct roles on the same physical dim.

### 4.3 The compiler — the one move that matters
**Aggregate each measure to its base grain inside a CTE first, THEN join the dimensions.** This is what neutralizes both fanout and chasm:

- **Fanout** (naive joins-then-sums → inflates): compiler aggregates `order_items` to its grain in a CTE, then joins up to `orders`/`stores` (many-to-one, no row multiplication).
- **Chasm** (orders has order_items AND refunds): each base metric (`total_revenue`, `refund_total`) gets its **own** CTE grouped by the requested dimensions, then the outer query joins the CTEs on the shared dimension keys and computes the derived metric — no cross-product.

The compiler also: resolves metric → measure expr; walks the join graph (BFS over many-to-one edges from the base entity; error if unreachable); injects partition/date filters for pruning; handles non-additive specially (distinct counts); offers **`explain`/compile-only** (return SQL without executing) for debugging and observability.

### 4.4 Time intelligence
A unified `metric_time` date spine; offset windows for YoY / prior-period; rolling/cumulative windows; grain-to-date; **explicit partial-period labeling** (a partial current month is data, not a drop).

### 4.5 Metadata = the agent's only map
Every metric carries: label, **includes/excludes description**, **synonyms** (indexed for NL retrieval), deprecation flag. Every dimension: human label + gloss for coded values. A **CI review gate**: no two metrics share a synonym; deprecated metrics flagged; every coded dimension has value glosses.

Definitions live in **version-controlled YAML** (`models/meridian.yaml`), reviewed like code; one edit propagates to every consumer.

---

## 5. Layer 2 — Grounding / Text-to-SQL

The model's job is narrowed to **one thing: pick the right metric(s) + dimensions.** It emits a **semantic query (intent object)**, never SQL. The layer owns joins/grain/aggregation.

### Semantic-query contract
```
metrics:    [names]            # what to compute
group_by:   [dimension names]  # how to slice
where:      [dim op value]     # filters against DIMENSIONS, not raw columns
time_grain: day|month|quarter|year
order_by, limit                # presentation only, never affect correctness
```
Every name must resolve to a validated definition; the model **cannot invent** a metric/dimension/join. A **validation gate rejects an invalid semantic query before any SQL runs.**

### Context engineering (the residual 10% is a context problem, not a model problem)
- **Metric retrieval** (cortexdb): embed `label + description + synonyms` per metric; at query time retrieve **top-5** definitions + their join paths. Never dump the whole schema (more context lowered accuracy 90→78%). Reindex in CI on model change.
- **Few-shot exemplars**: store `(question, semantic_query, pattern_tag)`; retrieve **top-3 deduped by pattern_tag** (diversity beats volume). Every fixed production miss is promoted into the bank and seeds the eval set.
- **Disambiguation as a feature**: if the top-2 metric retrieval score gap < threshold, or a required slot (time grain, entity) is missing → **ask back with concrete options**, never compile on a guess.
- **Honest decline**: if the right metric isn't modeled → "I don't have that metric," never improvise SQL. Goal: make all remaining failures **visible** (clarify/decline), never silent wrong numbers.

Target: the 40% → 90% accuracy jump; ~1 model call and ~5–10× cheaper per question than naive (pruned context + pruned scans).

---

## 6. Evaluation (the correctness control plane) — reuses **eval-go**

"Vibes are not a metric." The semantic layer makes eval *tractable*: score the chosen metric/dimensions, not SQL strings.

- **Eval set**: 100–200 labeled cases (`question`, gold semantic query, category tag, difficulty, expected answer) as versioned YAML; mined from real questions; stratified by failure mode + **governance probes** (must-deny / must-mask cases); weighted to real traffic; living.
- **Scoring**: **execution match** (primary, against a frozen deterministic snapshot — our `setseed` seed), **semantic match** (chosen metric/dims — for diagnosis), **result match** (a handful of board-critical anchors, e.g. total_revenue = 6,178,743.62). Never report exact-string match.
- **CI gates** (branch-protected): structural (model parses + references resolve) + behavioral (run eval set, `assert overall + per-category accuracy >= baseline`). Full set nightly; smoke subset on PRs; deterministic and fast.
- **Accuracy dashboard**: persist per-case results; trend, per-category, metric-confusion, latency/cost, worst-cases (looped back into the set).

eval-go provides the runner, deterministic + LLM-judge metrics, rate limiting, and CI exit codes; here the "judge" becomes **"is the number right"** (result match) plus governance-probe assertions.

---

## 7. Governance — policy in the layer, not the prompt

A prompt rule is advisory and trivially jailbroken ("for a payroll audit, list each salary…"). **Two enforced gates, belt-and-suspenders:**
- **Semantic layer** (compile time) decides *what a metric means* and *who may resolve it*.
- **Warehouse** (execution time) enforces masking + row filters — even a raw-SQL bypass hits these. **On disagreement, the engine wins.** Engine policies are generated *from* the layer definitions (one source of truth, two enforced copies).

Controls and where each lives:

| Control | Lives in | Mechanism |
|---|---|---|
| **Identity propagation** (keystone) | MCP/tool layer | the real human's role flows in the security context; **never a shared service account** — without this every control below is inert |
| Caller-scoped roles / least privilege | warehouse grants | each human → own role; agent inherits human limits |
| Metric RBAC (visibility) | metric def (`roles:`) + cortexdb `Authorize` | unauthorized metric → "not found", never enters the tool catalog. *You can't leak what the agent can't name.* |
| Row-level security | metric, bound to **live security context** (never a literal) + warehouse row policy | manager silently scoped to her region |
| Column masking | **dimension definition** + warehouse masking policy | redaction before the value leaves the layer |
| k-anonymity / thresholds | metric (`having count >= k`, null below k; raw metric `shown:false`) | k=5 default; blocks the re-identifying slice, allows legitimate aggregates |
| Prompt-injection defense | tool/agent layer | governed tools only (no raw-SQL hatch); delimit + mark retrieved data untrusted; lock metric descriptions in VCS; authorization caps any injection to caller grants |

The **threat model** is codified as YAML in VCS (each threat → control → owner → status) with a CI check that fails on any unaddressed threat. Reuses **compliant-rag**'s masking/audit/`Decide` patterns and **cortexdb**'s retrieval-layer `Authorize`.

---

## 8. Guardrails — containing the runaway agent

Three independent ceilings (set all three — each catches what the others miss):
1. **Time** — statement timeout on the agent's dedicated connection (warehouse default is often *days*; override).
2. **Data/spend** — bytes/credits cap per period; suspend at quota.
3. **Result** — row cap **in the tool layer** (code the agent can't negotiate past), not the DB.

Plus: **read-only by default** (withhold INSERT/UPDATE/DELETE/DDL at the grant level — structural, not a prompt request); **write-back only via propose-approve-commit** (agent emits a structured action + diff → human queue → separate audited writer; assert `token.principal != proposal.principal`); **full append-only audit trail** (identity, intent, semantic query + compiled SQL, rows/bytes/credits, guardrail trips), joined to engine query history via a query tag.

---

## 9. Layer 4 — MCP server (exposed last)

MCP standardizes only the **Agent→Tool** hop; it never moves accuracy by itself. **Never expose `run_sql`** (reopens every failure mode and bypasses governance). Expose **thick tools that wrap the semantic layer** — "a vending machine, not a key to the stockroom":

| Tool | Inputs | Returns |
|---|---|---|
| `list_metrics` | — | `[{name, description, dimensions}]` |
| `get_dimensions` | `metric` | valid dimensions for that metric (enables self-correction) |
| `query_metric` | `metric`, `group_by`, `filters`, `grain` | typed rows with **units + grain**, **row-capped** — maps to a semantic query, layer compiles SQL |

Tool rules: type hints → JSON-Schema; docstring → LLM-facing description with a concrete example; bad input → **recoverable structured error** with valid options ("Unknown metric 'sales' — did you mean 'revenue'?"); validate metric against `list_metrics`. Also expose **resources** (catalog, join graph, glossary) and **prompts** (finance recap, anomaly check).

### MCP security
- Server = **OAuth 2.1 Resource Server**: validate token signature, expiry, issuer, and **audience == this server** (RFC 8707); fail closed.
- **Confused-deputy cure**: never pass the client token through. Use a **token broker / on-behalf-of exchange** (RFC 8693) to mint a warehouse-audience credential; open the warehouse session as the **real user** so masking/row filters evaluate against the human. *Same tool + same compiled SQL → different governed results per user.*
- Scopes (least privilege; no write scope exists); secrets from a vault (short-lived, never in source); **multi-tenant isolation** derived from the token (never a tool arg), cache key = `tenant_id + metric + grain`; per-user/tenant rate limits (429 back-off).
- **Pen-test gate**: a 5-category red-team checklist (auth, identity, tenant, injection, guardrails) encoded as tests that fail CI if any attack succeeds; run nightly against staging (the spec keeps moving — pin the protocol version, keep transport/auth behind thin adapters).

Built on **agent-go**'s MCP support; the single semantic-layer adapter is the only component that knows the engine.

---

## 10. Agent loop — agentic analytics (reuses **agent-go**)

- **Decompose** only when a question has 2+ clauses (single-shot for one-metric asks); emit structured sub-questions `{intent, target_metric}`; reject any whose metric isn't in the model.
- **Plan → Query → Critique** loop, `max_iterations ≈ 3`, **no-progress guard** (open steps must shrink), plan-dedup. The **critic** re-reads the semantic model and checks **grain, coverage, sanity/range, metric-identity**; verdict `pass | revise | ask_user`; `revise` feeds the failure reason forward; cap-hit returns **partial findings, never a fabricated total.**
- **Typed conversation state** (~5 fields), follow-ups = field-level merge, topic-shift reset; every memory turn still passes the critique loop.
- **Chaining** (step N's result filters step N+1) with explicit dependency edges; each metric a separate governed call.
- **Patterns + red lines**: Conversational BI (read-only + a "receipt" showing metric def/filters/SQL), Scheduled/Triggered (pinned eval-covered questions, scoped identity, cost cap, idempotency, debounce, **explain-and-escalate-only — never act**), Write-Back (the guarded exception, propose-approve-commit). The 5 NEVERs: no source-of-truth writes, no DDL, no unattended writes, no freeform SQL writes, no skipping human approval.

---

## 11. Observability

- **Trace** = nested OTel spans over the 7 hops (question→agent→MCP tool→semantic query→compiled SQL→warehouse→rows), one `trace_id` propagated across processes via headers.
- **Structured logs** (JSON: trace_id, metric, filters, rows, latency, semantic-query *shape*) — queryable; **never log PII values** (column name + masked flag).
- **Cost/latency per span**: bytes_scanned, partitions, elapsed, warehouse_size, query_id (join engine history for credits); tag traces with `agent_name` + `question_category`. Targets: median < 3s, P95 < 8s.
- **Alerts**: credits/hour, P95 regression, error spikes, **result-cap-hit rate (silent truncation)** → with trace_id.
- **Debug discipline** = fix at the layer that owns the bug, never the prompt: rows wrong → model; total low → `result_cap_hit`, aggregate in layer; sentence wrong → formatting (+ eval check answer == row value); slow → caching.

---

## 12. Production hardening

- **Semantic drift** (compiles, but tastes wrong): 4 faces — structural / semantic / grain / join — all silent. Detection = run the eval gate against production **nightly** + source contract tests (freshness, not_null, accepted_values, cardinality). Fix upstream in the model.
- **Caching** keyed on **byte-identical SQL** (which grounding gives for free): warehouse result cache → semantic-layer cache → pre-aggregations; pre-warm hot metrics before business hours; lineage-driven invalidation; cache is never a second source of truth.
- **Versioning + safe rollout**: semantic model is a **versioned contract** (v1/v2 coexist, agents pin a version) → **shadow → canary 5% → 25% → 100%**, the eval gate between every rung **auto-halts** on failure. One eval harness guards merges + rollouts + drift.
- **Graceful degradation**: on timeout, return a labeled stale cached answer or honest "I can't answer reliably right now" — **never invent numbers.**
- **Operations runbook** in-repo: 4 paging SLOs (accuracy < 90%, cost over budget, P95 over target, availability); Detect→Triage→**Mitigate (rollback to last-green version pin)**→Resolve.

---

## 13. The 8-dimension defense rubric (launch gate — one red blocks launch)

| # | Dimension | Probe | Evidence |
|---|---|---|---|
| 1 | Semantic Model | "Show one revenue definition + the join graph" | single metric def + fanout-safe graph |
| 2 | Grounding | "Prove the LLM isn't writing raw SQL" | the semantic query + 40→90% |
| 3 | Evaluation | "What's your accuracy and how is it gated?" | result-match % + CI gate + dashboard |
| 4 | Governance | "Can the agent leak salary / another region?" | RBAC + RLS + masking + k-anon, all at the layer |
| 5 | MCP Security | "Whose identity reaches the warehouse?" | per-user propagation, no confused deputy |
| 6 | Autonomy | "What stops a runaway $40k bill?" | read-only default, cost caps, approvals, audit |
| 7 | Observability | "A board number is wrong — how fast do you find why?" | full trace, root cause in minutes |
| 8 | Drift | "A column was renamed last night — what happens?" | drift watcher + eval gate catch it |

Plus the "ninth muscle": **defensibility of the engine choice** (decision spine: openness, agent/MCP support, governance, multi-engine, cost, maturity, lock-in, pushdown). Our platform is effectively an *open-lane* engine: warehouse-agnostic via the Dialect abstraction, MCP-native, governance reimplemented in-layer.

### Five acceptance criteria
1. Accuracy ≥ 90% result-match on the live eval set.
2. Zero PII leaks; every answer scoped to the caller.
3. No single query scans past a hard byte ceiling.
4. Per-user identity reaches the warehouse (not a shared service account).
5. Survives the 8-dimension rubric.

---

## 14. Mapping to the Go ecosystem

| Platform layer | Reuses |
|---|---|
| Grounding LLM + agent loop + MCP server | **agent-go** v2.92.0 |
| Metric/exemplar retrieval; retrieval-layer `Authorize` security gate | **cortexdb** v2.33.0 |
| Eval set + accuracy/governance regression gates | **eval-go** v0.1.0 |
| RBAC/ABAC + PII masking + audit patterns | **compliant-rag** |
| Multi-engine SQL | `warehouse.Dialect` (Postgres now; Snowflake/Databricks/DuckDB next) |

### Package layout
```
semantic-mcp/
  deploy/        docker-compose Postgres + Meridian schema/seed         [done]
  warehouse/     database/sql + Dialect + cost guardrails               [done]
  semantic/      model types + YAML loader + compiler (aggregate-first) [model types done; compiler next]
  models/        meridian.yaml (the semantic model)                     [next]
  nl2sql/        grounding: retrieve → emit semantic query → validate
  governance/    RBAC/RLS/masking/k-anon/audit (compliant-rag patterns)
  mcp/           list_metrics / get_dimensions / query_metric + security
  agent/         decompose → plan-query-critique → memory (agent-go)
  eval/          eval set + gates (eval-go)
  obs/           tracing + cost/latency
  cmd/smctl/     CLI
```

---

## 15. Build order & status

1. ✅ Warehouse + Meridian normalized seed (Postgres, deterministic).
2. ✅ Warehouse connector + Dialect (Postgres) + cost guardrails.
3. ✅ Semantic model types (entities/dimensions/metrics/joins).
4. ▶ **Compiler** (aggregate-to-grain CTE → join graph → dialect SQL; `explain`) + `models/meridian.yaml`. **Verification milestone:** compile `total_revenue by store.region` → run on live Postgres → matches known-good per-region numbers; `net_revenue` proves chasm-safety.
5. Grounding (NL → semantic query) with cortexdb retrieval + disambiguation + decline.
6. Governance + guardrails (RBAC/RLS/masking/k-anon/cost/audit).
7. MCP server (3 thick tools) + security (identity propagation, audience binding).
8. Agent loop (plan-query-critique, memory, patterns).
9. Eval gates (eval-go) + observability + drift/caching/rollout.

**Through-line:** the **semantic layer** is the source of truth; the **eval gate** is the spine (guards merges, canary rollouts, and nightly drift with one harness); **identity must propagate, never flatten**; every failure is fixed at the layer that owns it, never in the prompt.
