# Agent-Ready Data Platform — Course Notes

*Semantic Layer, Text-to-SQL & MCP.* Notes distilled from the English subtitles of all **97 lessons across 23 modules**. Running example: **Meridian Retail** (a normalized retail warehouse).

---

## Table of Contents

**Part I — The Problem**
- [Module 1. The Mandate](#module-1-the-mandate)
- [Module 2. Why Naive Text-to-SQL Hallucinates](#module-2-why-naive-text-to-sql-hallucinates)

**Part II — The Semantic Layer**
- [Module 3. What a Semantic Layer Actually Is](#module-3-what-a-semantic-layer-actually-is)
- [Module 4. Modeling Metrics & Dimensions](#module-4-modeling-metrics--dimensions)
- [Module 5. The Join Graph](#module-5-the-join-graph)
- [Module 6. Choosing the Engine](#module-6-choosing-the-engine)
- [Module 7. Building the Semantic Layer](#module-7-building-the-semantic-layer)

**Part III — Grounding & Evaluation**
- [Module 8. The Grounding Jump (40%→90%)](#module-8-the-grounding-jump-4090)
- [Module 9. Context Engineering](#module-9-context-engineering)
- [Module 10. Evaluation](#module-10-evaluation)

**Part IV — Governance & Guardrails**
- [Module 11. The Governance Gap](#module-11-the-governance-gap)
- [Module 12. Governance at the Semantic Layer](#module-12-governance-at-the-semantic-layer)
- [Module 13. Guardrails](#module-13-guardrails)

**Part V — MCP**
- [Module 14. Why MCP](#module-14-why-mcp)
- [Module 15. Building the MCP Server](#module-15-building-the-mcp-server)
- [Module 16. MCP Architecture Decisions](#module-16-mcp-architecture-decisions)
- [Module 17. Securing MCP](#module-17-securing-mcp)

**Part VI — Agents & Operations**
- [Module 18. Agentic Analytics](#module-18-agentic-analytics)
- [Module 19. Agent Patterns](#module-19-agent-patterns)
- [Module 20. Observability](#module-20-observability)
- [Module 21. Production Hardening](#module-21-production-hardening)

**Part VII — Capstone**
- [Module 22. Capstone](#module-22-capstone)
- [Module 23. Wrap-Up](#module-23-wrap-up)

**Appendix — Reference Cards**
- [The 5-hop request](#appendix-reference-cards) · [5 failure modes](#appendix-reference-cards) · [Maturity ladder](#appendix-reference-cards) · [8-dimension defense rubric](#appendix-reference-cards)

---

# Part I — The Problem

## Module 1. The Mandate

> **Status:** ✅ Realized — 5-hop pipeline + maturity model in the platform. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

*"Just let them ask in English."* Meaning must live in the platform, not the model prompt.

1. **The CEO Email That Started It** — the same word ("revenue") has conflicting SQL across teams (finance=net, marketing=gross, ops=shipped). Fix = one governed metric defined once. This is a data-definition problem, not a smarter chatbot.
2. **The Three Naive Reactions (and Why Each Fails)** — buy-a-bot, fine-tune, prompt-stuff-the-schema all fail for one reason: no **encoded business meaning**. "Relevance beats volume."
3. **Anatomy of an Agent-to-Warehouse Request** — a question travels **5 hops** (intent → tool choice → SQL generation → execution → return); each can silently change the answer. Each hop needs an owner + a control.
4. **The Agent-Readiness Maturity Ladder** — L0 raw schema (joins guessed, ~40%) → L1 documented schema → L2 semantic layer (~90%) → L3 governance travels with the layer → L4 MCP as the governed, observable contract.

**Implement:** a governed metric registry (single source of truth) + the 5-hop pipeline with explicit, controllable boundaries; join correctness is a platform guarantee, not the LLM's job.

## Module 2. Why Naive Text-to-SQL Hallucinates

> **Status:** ✅ Prevented — compiler structurally blocks all 5 failure modes. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Valid SQL ≠ correct SQL — the engine checks grammar, never meaning.

5. **Live: Watch It Hallucinate a Join** — orders (1/order) ⋈ order_items (many/order); summing `order_total` after the join multiplies by line count → ~4× inflation, clean run, green check.
6. **The Five Failure Modes** — (1) wrong join, (2) wrong grain, (3) fan-out, (4) ambiguous metric, (5) silent wrong answer.
7. **Why "Just Give It the Schema" Doesn't Work** — a 380k-token schema buries signal ("lost in the middle"); naming chaos (`custid`/`customer_id`/`custed`); DDL never states meaning (net vs gross, roll-ups).
8. **The Silent-Wrong-Answer Problem** — "a crash is a gift"; a silent wrong number looks identical to a correct one and erodes trust. Prevent upstream, don't detect after.

**Implement:** seed the star schema with the `order_total = Σ line_total` trap; structurally prevent all five failure modes; bias to prevention over detection.

---

# Part II — The Semantic Layer

## Module 3. What a Semantic Layer Actually Is

> **Status:** ✅ Built — `semantic-go` (typed entities/dims/metrics + join graph), headless via engine/MCP/HTTP. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

A contract between business meaning and SQL, holding one governed definition everyone shares.

9. **The Missing Layer** — "revenue" lives in 40 dashboards / 12 transform models / people's heads and drifts. Define it once in version-controlled YAML; the layer compiles it identically every time.
10. **Metrics, Dimensions, Entities — The Three Nouns** — every question = a **metric** (what), a **dimension** (how to slice, typed), an **entity** (who/what, with a primary key). Typed → invalid combos are rejected, not hallucinated.
11. **The Join Graph: Encode Relationships Once** — nodes=entities, edges=declared relationships with keys + cardinality. The layer only traverses declared edges; a missing edge is **refused**, never invented. "A refused join is a feature." Highest-leverage fix.
12. **Semantic Layer vs BI Layer vs Raw SQL** — raw SQL drifts; BI logic is locked in a proprietary tool agents can't read; the semantic layer is readable by code, BI, and agents alike.
13. **Headless and Multi-Consumer** — the model is just an API (SQL/GraphQL/REST/SDK/MCP). One definition, many interfaces. "A new consumer means a new interface, never a new definition."

**Implement:** typed Entity/Dimension/Metric + a typed join graph; compile to SQL; serve headless to many consumers; multi-engine dialect abstraction.

## Module 4. Modeling Metrics & Dimensions

> **Status:** ✅ Built — simple/ratio/derived + **time intelligence** (rolling/cumulative/delta/prior) + **grain-to-date** (`reset: year|quarter|month` → window restarts each period; `revenue_ytd` verified resetting to 245,384.21 at 2025-01) + **additivity enforcement** (`additivity: additive|semi_additive|non_additive`, inferred from agg/formula; the compiler refuses a window-sum over a non-additive measure) + **metadata gate** (`semantic.Lint` / `di model lint`, exits 1 on a metric missing its description — CI-gateable). All in the neutral `semantic-go` core; Meridian only supplies config. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

A metric carries its grain and aggregation so totals never double count.

14. **Your First Metric, Defined Once** — distinguish **measure** (raw aggregation on one table) from **metric** (business-facing wrapper: simple/ratio/derived/cumulative).
15. **Grain, Additivity, and the Fan-out Trap** — additive (sum anywhere) / semi-additive (`last_value` over time) / non-additive (ratios, distinct counts). Pin each measure to its grain so fan-out can't happen.
16. **Descriptions, Synonyms, and Business Context** — metadata is the agent's only map. Describe what a metric **includes/excludes**; route **synonyms** to one metric; gloss coded dimension values. A CI review gate enforces this.
17. **Time Intelligence Without Hand-Written SQL** — a shared `metric_time` spine; YoY/prior-period via offset windows; rolling/cumulative windows; grain-to-date; label partial periods.
18. **Debug: A Metric That Returns the Wrong Number** — 5-step playbook: reproduce → read compiled SQL (`explain`) → confirm on one known row → fix grain in the model (never patch the dashboard) → lock with a reconciliation test in CI.

**Implement:** measure/metric split; metric types (simple/ratio/derived/cumulative); per-measure additivity; time intelligence; `explain`; reconciliation tests.

## Module 5. The Join Graph

> **Status:** 🟡 Core done — aggregate-then-join + refusal; bridge (m:n) & role-playing dims not built. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Killing hallucinated joins by encoding relationships once.

19. **One Bad Join = One Wrong Board Number** — fan-out staples a full order total onto every line item; `Σ order_total` counts it N times. Fixing the graph kills the whole class.
20. **Encoding Relationships Correctly** — declare cardinality per edge; many-to-many goes through a **bridge table** (two many-to-one edges); model **role-playing** dimensions (order_date vs ship_date) distinctly.
21. **Fanout and Chasm Traps** — fanout = a join multiplies rows; chasm = two independent one-to-many children off one parent → Cartesian. Cure: **aggregate each measure to its base grain first, then join** (each branch in its own CTE).
22. **Before and After: Same Question, Two Answers** — naive `Σ order_total` = 19.2M; grounded metric = 5.1M (matches finance). "The only difference is who owns the join — the LLM or the graph."

**Implement:** join graph as declared metadata (keys + cardinality); aggregate-then-join compiler; bridge tables; role-playing dims; fail-to-compile on a missing edge.

## Module 6. Choosing the Engine

> **Status:** 🟡 Partial — Dialect abstraction + Postgres; Snowflake/Databricks dialects not built. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

The semantic-layer decision spine.

23. **The Contenders** — open (dbt Semantic Layer/MetricFlow, Cube) vs warehouse-native (Snowflake Semantic Views + Cortex Analyst, Databricks Metric Views + Genie) vs BI-native (LookML).
24. **The Decision Spine Matrix** — 8 axes: openness, agent/MCP support, governance, multi-engine reach, cost, maturity, lock-in, query pushdown. Score 1–5; lock-in & cost are inverted; weight per context.
25. **Three Scenarios, Three Right Answers** — single warehouse → native; dbt shop → dbt SL; mixed estate → open layer (Cube). Same scores, different weights flip the winner.
26. **Defend the Choice** — a 5-section decision record (context/options/weights/decision/consequences); every objection maps to an axis; "it's modern" is not a defense.

**Implement:** stay engine-agnostic (one model → many dialect targets); reimplement governance an open layer doesn't get for free; keep a written, versioned decision record.

## Module 7. Building the Semantic Layer

> **Status:** ✅ Built — warehouse + model + query interface + reconciliation (`di query`, `di eval`). · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Assemble it over a real warehouse and validate to the cent.

27. **Project Setup and Connecting the Warehouse** — 3 layers (warehouse=data, layer=meaning, consumers); credentials from env only; agents never touch the warehouse directly.
28. **Model the Meridian Retail Star** — declare entities (primary/foreign keys), measures at the correct grain, dimensions, time dimension; a governed `total_revenue` defined once.
29. **Expose Metrics via the Query Interface** — three front doors (SQL/GraphQL/REST), all resolving to the same definition; request a metric by name, never a hand-written join.
30. **Validate Against Known-Good Numbers** — reconcile to a finance baseline to the cent; consistent NULL handling; slice-level reconciliation (FULL JOIN + per-row diff), not just totals.
31. **Recap and the Model We Will Feed the LLM** — the semantic model (not the 1200 raw tables) is what the LLM reads. Grounding = pointing the model at the semantic model.

**Implement:** connector + connectivity check; YAML model authoring; 3 query interfaces; a reconciliation subsystem that fails on drift.

---

# Part III — Grounding & Evaluation

## Module 8. The Grounding Jump (40%→90%)

> **Status:** 🟡 Mostly done — NL→semantic query (`di ask`); **accuracy batch now measured** by `di nleval` (the 40→90 grounded-vs-naive batch, scored against control SQL). missing: cost benchmark. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Narrow the model's job to picking the right metric.

32. **Re-run Module 2's Failures, Now Grounded** — the agent emits a semantic query (names a metric, not a join); the layer compiles one correct SQL. Batch: naive 8/20 (~40%) → grounded 18/20 (~90%).
33. **How Grounding Works** — the model emits a tiny **intent object** (`metrics`, `group_by`, `where` against dimensions, `order_by`, `limit`); it cannot invent metrics/joins; validation rejects bad queries before any SQL runs.
34. **Semantic Query to Compiled SQL** — one-line intent → ~30 lines of fan-out-safe SQL; the compiler aggregates to grain in a CTE first, then joins; the pattern is portable across engines.
35. **The Production-Economics Benchmark** — grounded ≈ 1 model call + pruned scans ≈ 5–10× cheaper per question than naive; the biggest cost is wrong answers.
36. **Where the 10% Still Breaks** — remaining failures are ambiguity (ask back), missing metric (decline honestly), and novel questions — now **visible**, not silent.

**Implement:** the grounded pipeline (retrieve relevant metrics → emit intent → validate → compile → execute); make residual failures visible (clarify/decline).

## Module 9. Context Engineering

> **Status:** ✅ Built — **hybrid retrieval** (cortexdb BM25 ⊕ dense embeddings via DashScope `text-embedding-v4`, in-process cosine fusion) + **few-shot exemplar bank** (`models/exemplars.yaml`: embedding-retrieved, shape-deduped, injected into the prompt; `di exemplar` promotes fixed misses) + clarify gate (`grounding/`, `di ask`). Verified: "running total of sales over time" → window metric `revenue_cumulative`; NL eval 12/12 with LLM. missing: cross-encoder rerank wiring. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

After grounding, the residual ~10% is a context problem, not a model problem.

37. **Context Is the New Limiting Factor** — more context ≠ better (pasting everything dropped 90→78%). Budget tokens across system rules / metrics / exemplars / history.
38. **Retrieving the Right Metrics** — RAG over metric metadata (embed label+description+synonyms); retrieve top-5 definitions + join paths; reindex on every model change.
39. **Few-Shot and Query Exemplars That Generalize** — store (question → semantic query) exemplars; retrieve top-3 deduped by pattern; diversity beats volume; promote every fixed miss into the bank.
40. **Disambiguation and Clarifying Questions** — if top-2 metrics tie or a required slot is missing, ask back with concrete options — never compile on a guess.
41. **Debug: The Agent Picked the Wrong Metric** — reproduce with similarity scores; fix the **metadata** (not the prompt); re-index; add the case to the exemplar bank + eval set.

**Implement:** metric retrieval index + exemplar bank + disambiguation gate; fix data (metadata), not prompts.

## Module 10. Evaluation

> **Status:** ✅ Built — NL eval closed-loop (`di nleval`, `nleval/`): labeled set, three-axis scoring (execution / semantic / result-match vs control SQL), governance probes, metric-confusion matrix, per-category CI gate (exits 1 on regression), accuracy dashboard (`_nl_eval_runs`, `GET /nleval`), eval-go LLM-judge groundedness layer. Verified 11/11 offline. (left: cost/latency-per-question panel) · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

"Vibes are not a metric."

42. **You Can't Ship What You Can't Measure** — grade by **execution match** (headline), **semantic match** (chosen metric/dims — diagnosis), **result match** (board-critical anchors). Never report exact-string match.
43. **Build an Eval Set From Your Own Questions** — 100–200 labeled cases mined from Slack/BI logs/failed-bot logs; stratified by failure mode + governance probes; weighted to real traffic; a living asset.
44. **Regression Gates in CI** — structural gate (parse + validate-configs) + behavioral gate (run eval set, assert accuracy ≥ baseline + per-category floors); required check, frozen data snapshot.
45. **The Accuracy Dashboard** — persist per-case results to a table; trend, per-category, metric-confusion, cost/latency, worst-cases (fed back into the set).

**Implement:** labeled eval set + execution/semantic/result scoring + CI gates + accuracy dashboard.

---

# Part IV — Governance & Guardrails

## Module 11. The Governance Gap

> **Status:** ✅ Built — RBAC/masking/audit + **threat-model-as-code** (`governance/threatmodel.go`, `examples/meridian/threats.yaml`, `di threats`): 10 threats each mapped to a control + owner + evidence; the CI gate fails on any open/under-specified threat. Verified 10/10 addressed. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Accurate ≠ authorized. Agents are a new data consumer = new attack surface.

46. **The Agent That Saw Everyone's Salary** — an accurate answer that was never authorized; the human identity vanished at the tool boundary (shared service account).
47. **Over-Permissioning and the Service-Account Trap** — one God-mode `agent_service` with SELECT on everything; the fix is caller-scoped roles + identity propagation. Least privilege.
48. **Prompt Injection Into Your Data Layer** — warehouse content is an input channel; a poisoned row/comment becomes instructions (indirect prompt injection). No trust boundary between data and instructions.
49. **The Threat Model** — assets / actors / trust boundaries; a threat→control table (data leak, privilege escalation, prompt injection, cost runaway, silent wrong answer); codify as YAML + a CI check.

**Implement:** answer WHO (real human) / WHAT (may see) / WHY (logged) for every query; threat-model-as-code.

## Module 12. Governance at the Semantic Layer

> **Status:** ✅ Built — RBAC + masking + **RLS (attr-bound, fail-closed)** + **k-anonymity (cohort suppression)** + audit in `governance.Query`, **plus warehouse-level RLS** (belt-and-suspenders): the on-behalf-of session does `SET LOCAL ROLE` + sets `app.region`, and a Postgres row-security policy on `stores` enforces it — the engine scopes the user even if the app layer misses (`di obo`, `warehouse.QueryAs`). · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Policy belongs in the layer, not the prompt — enforced at compile time **and** at the warehouse (belt-and-suspenders; the engine wins).

50. **Policy Belongs in the Layer, Not the Prompt** — prompts are advisory and jailbroken; the layer decides what a metric means and who may resolve it; the warehouse enforces masking + row filters under it.
51. **RBAC and Governed Metrics** — bind metrics to roles; an unauthorized agent gets "metric not found" — you can't leak what it can't name. Identity must propagate, not flatten.
52. **Row-Level Security and Column Masking** — RLS scopes rows, masking scopes values; both travel with the **metric** (the agent skips the dashboard); bind filters to the live security context, never a literal.
53. **PII, Aggregation Thresholds, and k-Anonymity** — small cohorts re-identify people; set k=5; enforce in the metric (`having count >= k`, null below); make the safe metric the only path.
54. **Snowflake and Databricks Governance Mapping** — map the same policy to each engine's primitives (roles/grants, masking policy, row access policy; ABAC governed tags at scale).

**Implement:** metric RBAC + RLS (security context) + column masking (in the dimension) + k-anonymity; generate engine policies from the layer.

## Module 13. Guardrails

> **Status:** ✅ Built — read-only sessions + timeout + row cap + write-approval (flow) + **pre-execution byte ceiling** (`warehouse.Estimate`/`GuardCost` via `EXPLAIN`; `DI_MAX_SCAN_BYTES` refuses an over-budget query before a row is read — verified) + estimated cost recorded on the trace. Missing: per-tenant spend accounting over time. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Containing the runaway agent (the $40k weekend query).

55. **The $40,000 Weekend Query** — an unwatched plan→retry loop ran 1,800 full scans. Defense in depth across query / warehouse / agent layers.
56. **Query Cost Limits, Timeouts, and Result Caps** — three ceilings: time (statement timeout), data (credit/byte cap), result (row cap **in the tool**, agent can't override). "Trust the engine, never the prompt."
57. **Read-Only by Default, Approvals for the Rest** — default-deny writes via grants (structural, not prompt); legitimate writes go through propose→approve→commit (separation of duties).
58. **The Full Audit Trail** — log identity / intent / semantic query + compiled SQL / outcome (rows, bytes, credits, guardrail trips) to an append-only store; stitch app log to engine query history via a query tag.

**Implement:** three independent cost ceilings; read-only default + write approval; append-only audit joined to engine history.

---

# Part V — MCP

## Module 14. Why MCP

> **Status:** ✅ Built — MCP wraps the layer (official go-sdk). · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

The standard contract between agents and data.

59. **From N×N Custom Integrations to One Protocol** — one server per source, one client per agent (N+M, not N×M). MCP is the integration layer, not the meaning layer.
60. **Tools, Resources, and Prompts** — tools (model-controlled verbs), resources (app-controlled read-only context), prompts (user-controlled templates). **Never expose a `run_sql` tool.**
61. **Where MCP Sits vs the Semantic Layer** — MCP standardizes *how* the agent reaches data; the semantic layer defines *what it means* and *who may see it*. Adding MCP alone moves accuracy by zero.
62. **The Spec Is Moving: Durable vs Temporary** — durable: JSON-RPC, the 3 primitives, capability negotiation, N×M value. Temporary: transport, auth, field names → wrap them behind thin adapters; pin the version.

**Implement:** MCP wraps the semantic layer (governance inherited); build on durable parts, adapter-wrap the moving ones.

## Module 15. Building the MCP Server

> **Status:** ✅ Built — `di mcp`: list_metrics/get_dimensions/query_metric, no run_sql. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Wrap the semantic layer as thick tools.

63. **Your First MCP Server** — type hints → input schema, docstring → description; return structured output; stdio for local.
64. **Wrapping the Semantic Layer as Tools** — three tools form the loop: **list_metrics**, **get_dimensions**, **query_metric** — "a vending machine, not a key to the stockroom." A thin SQL proxy throws away the whole layer.
65. **Official Servers vs Rolling Your Own** — check official (dbt-mcp, Snowflake, Databricks) first; for a mixed estate build a federation server that reuses them inside.
66. **Tool Design for LLMs** — the agent picks from names/descriptions/schemas; a few business-named verbs; recoverable structured errors ("did you mean…"); typed return with units + grain; cap rows.
67. **Test With a Real Agent Client** — register the server, auto-discover tools, run the full loop (list → get_dimensions → query_metric); script it so it runs in CI.

**Implement:** the three thick tools, no `run_sql`; LLM-friendly descriptions + recoverable errors; a single semantic-layer adapter; test via a real client.

## Module 16. MCP Architecture Decisions

> **Status:** 🟡 Partial — stdio; remote/streamable HTTP & ADR not built. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

68. **Local vs Remote, One Server vs Many** — local stdio (one user) vs remote HTTP (shared, central governance); monolith vs domain fleet (blast radius vs ops cost). Co-locate near the warehouse.
69. **Transport and Auth Choices** — stdio (dev) vs streamable HTTP (remote; HTTP+SSE is deprecated); the server is an OAuth 2.1 Resource Server (Protected Resource Metadata, Resource Indicators).
70. **Tool Granularity: Thin Proxy vs Thick Semantic Tools** — the single most consequential decision (accuracy + governance + cost). Choose thick; gate any escape hatch (read-only, role-scoped, byte-capped, audited).
71. **The MCP Decision Spine** — six dimensions (deployment, transport, auth, granularity, blast radius, operability), each with a defended reason, recorded as a dated ADR.

**Implement:** remote-in-VPC + PII carve-out; streamable HTTP; broker auth; thick tools; one ADR.

## Module 17. Securing MCP

> **Status:** ✅ Built — **real OIDC/JWT verification** (`mcp/oidc.go`, `di mcp -oidc`): stdlib RS256 via JWKS or static key, issuer/audience/exp/nbf checks (audience = confused-deputy cure, RFC 8707), claims→principal, 401 on reject; `di token` dev issuer **+ RFC 8693 on-behalf-of** (`mcp.ExchangeToken`: re-scope to warehouse audience, never forward the client token) **+ per-user DB session** (`warehouse.QueryAs`: `SET LOCAL ROLE` least-priv + `app.*` GUCs → **Postgres RLS** scopes the real user; `di obo setup/demo/chain`). Live: manager session → only its region, admin → all; nleval 11/11 no regression. Plus scopes + per-principal rate limit + tenant claim **+ automated pen-test gate** (`di pentest`, lesson 75): fires 9 forged tokens (expired / nbf-future / wrong-audience / untrusted-issuer / forged & tampered signatures / malformed / missing) at the real verifier plus an unauthorized-metric probe, exits 1 if any control is breached — verified all rejected. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

72. **The Confused-Deputy Problem** — the server uses *its* authority for the *caller's* request; many users behind one agent collapse to one privilege. Cure: validate token audience (RFC 8707) names this server.
73. **Per-User Identity Propagation and Scopes** — never pass the client token through; use on-behalf-of exchange (RFC 8693) to mint a warehouse-audience token; open the session as the real user so Module-12 policy fires. Map each tool to a minimum scope.
74. **Secrets, Multi-Tenant Isolation, and Rate Limits** — secrets from a vault (short-lived, scoped); derive tenant from the token (never an arg); partition cache keys by tenant; per-user/tenant rate limits (429).
75. **Pen-Test Your Own Server** — a red-team checklist (auth, identity, tenant, injection, guardrails) encoded as tests that fail CI if any attack succeeds; run nightly against staging.

**Implement:** audience-bound token validation + identity propagation + scopes + tenant isolation + rate limits + a pen-test gate.

---

# Part VI — Agents & Operations

## Module 18. Agentic Analytics

> **Status:** ✅ Built — agent-go agent (`di agent`) **+ plan-query-critique loop** (`critic/`, `di ask -critic`): rule critic (coverage/sanity/metric-identity) ⊕ LLM critic (grain/identity) → pass|revise|ask_user, feedback-fed re-grounding, bounded retries + cycle guard + graceful degradation (lessons 77/80) **+ typed cross-turn memory** (`convo/`, `di chat`): field-level merge incl. WHERE filters / topic-shift reset + per-thread sub-result cache (lesson 78) **+ multi-metric chaining** (`di chain`): ordered steps with `${stepID}` dependency edges, pick top/bottom → next step's filter (lesson 79). All verified live. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

From one query to a guided multi-step thread.

76. **Single-Shot vs Multi-Step Reasoning** — decompose into structured sub-questions (`intent` + `target metric`); only when 2+ clauses; reject any step whose metric isn't in the model.
77. **The Plan-Query-Critique Loop** — plan → query → critique → loop or answer; bounded retries (max ~3), no-progress guard; the critic checks grain / coverage / sanity / metric-identity; verdict pass | revise | ask_user.
78. **Memory and Conversation Context** — keep a small typed query state (~5 fields), not the transcript; follow-ups = field-level merge; topic shift resets; every memory turn still passes the critic.
79. **Multi-Metric, Multi-Step Questions** — chain step N's result into step N+1's filter (explicit dependency edges); each metric a separate governed call; cache sub-results within a thread.
80. **Debug: The Agent Looped or Gave Up** — loop-control bugs, not model intelligence: feed the critic's reason forward; no-progress guard; plan-dedup; wire empty retrieval to disambiguation. Always attach the iteration trace.

**Implement:** decomposer + plan-query-critique loop + typed memory + chaining; debug via the trace.

## Module 19. Agent Patterns (and When NOT to Use Them)

> **Status:** ✅ Built — **conversational BI** (`di chat`) + **production write-back** (`writeback/`, `di propose/approve/reject/revert`): NL → **typed** change proposal (never raw SQL) confined to a **writable allowlist** (`models/writeback.yaml`), dry-run with before-image + affected-row count, **propose→approve→commit** with **separation of duties** (proposer ≠ approver, role-gated), parameterized SQL + red-line wall + max-affected cap, **atomic apply + rollback from before-image**, full audit — covers the whiteboard's **CRUD gen** (insert/update/delete) AND **transform gen** (NL → compile-validated metric merged into the model with backup). Verified live: UPDATE/INSERT/DELETE + revert + a generated `avg_refund_per_order` metric. Missing: scheduled/triggered agents. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

81. **Conversational BI** — a read-only chat agent (`list_metrics`/`query_metric`/`describe_metric`) that echoes a "receipt" (metric def + filters + show-SQL); resolve identity before any query.
82. **Scheduled and Triggered Agents** — unattended raises the stakes (more guardrails); run a pinned, eval-covered question list; triggers only **explain + escalate**, never act; idempotency + cost caps + debounce.
83. **Write-Back and Actions — The Danger Zone** — wrong write = corrupt data forever. The 5 NEVERs; safe shape = propose→approve→commit to a write-back schema, typed parameterized actions, human approval, full audit; proposer ≠ executor.
84. **Pattern Selection** — a decision tree (human reading? mutates state? conversation? scheduled?) + a red-line matrix; refusal is a valid answer.

**Implement:** the four patterns with their mandatory guardrails; write-back stays the rare, gated exception.

## Module 20. Observability

> **Status:** ✅ Built — **real OpenTelemetry** (`obs/otel.go`, `DI_OTEL=1`): a TracerProvider with a JSON span exporter emits a true span tree per request — `governed_query → compile → plan → execute` under one `trace_id`, with **cost attributes** (est_rows/est_bytes) alongside latency — plus **W3C trace-context propagation** (`InjectMap`/`ExtractMap`/`ExtractHTTP`): the MCP HTTP handler continues the client's trace, so server-side spans nest under the caller's distributed trace (verified via `di trace`: client span → `governed_query` parent_span_id matches, single trace_id end to end). The custom `_traces`/`/traces` dashboard + `_audit` remain. Instrumentation is a no-op until OTel is enabled (zero cost off). · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Trace the wrong answer back.

85. **The Full Trace** — a question crosses 7 boundaries (question → agent → MCP tool → semantic query → compiled SQL → warehouse → rows); a trace links them via one trace_id; 5 places answers betray you.
86. **Logging and Tracing the Stack** — log meaning, not just exceptions (JSON with trace_id, metric, filters, rows); never log PII values; instrument the tool span + a warehouse-cost child span; propagate trace context across processes.
87. **Cost and Latency Monitoring** — per-trace cost (tokens + credits + latency); tag by agent + question category; alert on credits/hour, P95 regression, result-cap-hit rate; one dashboard: accuracy + cost + latency.
88. **Debug Clinic: Three Wrong Answers** — fix at the layer that owns the bug (model / config / agent / cache), never the prompt: gross-vs-net → model; silent truncation → aggregate in layer; wrong sentence → formatting + eval check.

**Implement:** OTel traces/spans across the stack; structured logs (PII-safe); cost/latency per trace; layer-ownership debug discipline.

## Module 21. Production Hardening

> **Status:** ✅ Built — drift gate (`di eval`), result cache + graceful degradation, shadow rollout (`di shadow`), runbook, **plus the full change-management plane** (`rollout/`, `di rollout`): persisted **version registry**, deterministic **canary traffic-split** (verified 20%→20.4% over 1000 reqs), **lineage-driven invalidation** (promote diffs metric definitions → invalidates only changed metrics' caches), and one-command **rollback**. Missing: automated canary metric-watcher that auto-rolls-back. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

89. **Semantic Drift** — model and warehouse fall out of sync with no error (structural / semantic / grain / join). Detect by running the eval gate against production nightly + source contract tests.
90. **Caching and Latency** — three tiers (warehouse result cache → semantic-layer cache → pre-aggregations); stable/byte-identical SQL (which grounding gives free) makes caching hit; lineage-driven invalidation; cache is never a source of truth.
91. **Reliability, Fallbacks, and Safe Rollout** — version the model (v1/v2 coexist, pin a version); shadow → canary 5% → progressive, eval gate auto-halts; graceful degradation: labeled stale answer or honest "I can't answer", never invent numbers.
92. **The Operations Runbook** — 4 paging SLOs (accuracy / cost / latency / availability); Detect → Triage → **Mitigate (rollback to last-green version)** → Resolve; runbook lives in the repo; quarterly game-day.

**Implement:** nightly drift gate + contract tests; 3-tier caching; versioning + canary + auto-halt; graceful degradation; in-repo runbook.

---

# Part VII — Capstone

## Module 22. Capstone

> **Status:** 🟡 Spine + breadth prototype; acceptance criterion #1 (**accuracy ≥90% result-match**) now measured by `di nleval` (11/11 offline) with a CI gate. Remaining depth gaps per §16. · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

Ship and defend an agent-queryable semantic layer.

93. **The Brief: Meridian Goes Live Company-Wide** — five acceptance criteria: accuracy ≥90% result-match; zero PII leaks scoped to caller; hard byte ceiling; per-user identity to the warehouse; survives the 8-dimension rubric.
94. **Build: Model → Grounding → Governance → MCP** — assemble in dependency order (meaning first, transport last); each layer inherits the guarantees beneath it.
95. **Evaluate and Harden** — replay the eval set, score result-match (91%/120), tighten demo-loose settings to production-strict, attack live (the salary question is refused), debug a planted failure via the trace, roll out behind a drift check + canary.

## Module 23. Wrap-Up

> **Status:** ✅ 8-dim rubric, no red: Semantic Model / Grounding / Evaluation (47-case set, 100% offline + CI gate) / Governance (threat-as-code) / MCP-Security (`di pentest`: 9 forged-token attacks rejected) / Autonomy (write-back + byte ceiling) / Drift (version registry + canary + lineage invalidation) / Observability (real OTel: span tree + cost attrs + W3C cross-process propagation) all green. The spine is production-grade; remaining polish is breadth (extra warehouse dialects, auto-rollback canary watcher, grounding cross-encoder rerank). · [→ DESIGN §16](DESIGN.md#16-status--remaining-work-honest-vs-this-designs-full-depth)

96. **The 8-Dimension Defense Rubric** — for each: a probe question + the evidence. **(1) Semantic Model** (one revenue def + join graph) · **(2) Grounding** (semantic query, 40→90) · **(3) Evaluation** (accuracy % + CI gate) · **(4) Governance** (RBAC/RLS/masking/k-anon, all in the layer) · **(5) MCP Security** (per-user identity, no confused deputy) · **(6) Autonomy** (read-only, caps, approvals, audit) · **(7) Observability** (full trace, root cause in minutes) · **(8) Drift** (drift watcher + eval gate). A single red blocks launch. Ninth muscle: defend the engine choice.
97. **Your Roadmap From Here** — naive (40%) → semantic layer (90%) → governance/guardrails → MCP → observability. Frontier: residual 10%, multi-agent + write-back, a moving spec, eternal drift. "Agent-ready is a posture you maintain, not a milestone you finish." 4-week roadmap: (1) define one contested metric, (2) log the join graph for the top 3 questions, (3) build a 50-question eval set, (4) wrap it in a governed, identity-propagated MCP server.

---

# Appendix — Reference Cards

**The 5-hop request:** intent → tool choice → SQL generation → execution → return. Each hop can silently change the answer; each needs an owner + a control.

**The 5 failure modes:** wrong join · wrong grain · fan-out · ambiguous metric · silent wrong answer. (Valid SQL ≠ correct SQL.)

**The maturity ladder:** L0 raw schema (~40%) → L1 documented schema → L2 semantic layer (~90%) → L3 governance in the layer → L4 governed, observable MCP.

**The compiler's one move:** aggregate each measure to its base grain in a CTE first, then join — neutralizes fan-out and chasm.

**The 3 MCP tools:** `list_metrics` · `get_dimensions` · `query_metric`. Never `run_sql`.

**The 8-dimension defense rubric:** Semantic Model · Grounding · Evaluation · Governance · MCP Security · Autonomy · Observability · Drift. One red blocks launch.

*Source: English subtitles of the 97-lesson course, under `~/Downloads/Agent-Ready Data Platform Semantic Layer, Text-to-SQL & MCP/`.*
