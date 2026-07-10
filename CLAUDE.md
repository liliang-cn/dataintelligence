# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

DataIntelligence is a governed semantic layer + MCP gateway that makes a data
warehouse safe for AI agents. The thesis: **AI decides *what* to ask; a
deterministic layer guarantees the answer is *correct*.** Letting an LLM write
SQL directly is ~40% accurate; here the LLM only emits a typed *semantic query*
(metric × dimensions × filters), and the compiler turns that into
fanout/chasm-safe SQL. Build order is load-bearing: **meaning first, transport last.**

Module: `github.com/liliang-cn/dataintelligence` (Go 1.25). Single CLI binary `di`.

## Commands

```bash
go build ./...                      # build everything (pure Go, CGO-free)
go test ./...                       # all unit tests
go test ./nleval/ -run TestX        # a single test
go vet ./...

go build -o /tmp/di ./cmd/di        # build the CLI

# DuckDB execution backend is opt-in (CGO); the default build excludes it:
CGO_ENABLED=1 go build -tags duckdb ./cmd/di
CGO_ENABLED=1 go test -tags duckdb ./warehouse/
```

A local Postgres warehouse is required for the warehouse-backed gates:

```bash
cd deploy && docker compose up -d   # Postgres :39632 (Meridian schema + seed auto-applied) + ES
# default DSN: postgres://meridian:meridian@localhost:39632/meridian?sslmode=disable
```

The CLI **is** the test harness for end-to-end behavior — these gates must stay green:

```bash
di eval        # reconciliation: every metric equals a hand-written control query
di nleval      # NL accuracy gate over models/nl_evalset.yaml (per-category floor; exits 1 on regression)
di model lint  # metadata gate (every metric needs a description)
di threats     # threat-model-as-code gate (examples/meridian/threats.yaml)
di pentest     # MCP security regression (forged-token battery)
```

`nleval`/`copilot`/`reconcile -ai`/`model gen -llm` use an LLM only when
`LLM_BASE_URL` / `LLM_API_KEY` / `LLM_MODEL` are set (and `DI_EMBED_*` for dense
retrieval); otherwise they fall back to deterministic paths. Without those, the
10 `needs_llm` eval cases are skipped, grounding uses the keyword matcher, etc.

## Architecture

The request path (each hop has an owner; governance applies on every hop):

```
agent/app → grounding (NL→semantic query) → semantic-go compiler (→ safe SQL) → warehouse
```

- **`semantic-go`** (separate published module, in the go.work workspace): the
  semantic model (entities/dimensions/metrics/join-graph) and the compiler. The
  one move: aggregate each measure to its grain in its own CTE, *then* join —
  fanout/chasm impossible by construction. Dialects: Postgres/Snowflake/Databricks/DuckDB.
  When a capability is generic to the semantic layer, it belongs here, not in this repo.
- **`engine`** — ties the compiler to the warehouse; routes a `duckdb:` DSN to the
  DuckDB backend, else Postgres; runs the pre-execution cost estimate.
- **`warehouse`** — executes SQL under guardrails (timeout, row cap, byte ceiling
  via `EXPLAIN`); `QueryAs` sets `SET LOCAL ROLE` + `app.*` GUCs so Postgres RLS
  scopes the real user (on-behalf-of). DuckDB backend is build-tagged (`duckdb.go`).
- **`grounding`** — NL → semantic query: cortexdb BM25 ⊕ dense embeddings ⊕
  cross-encoder rerank + few-shot; LLM generation with a deterministic keyword fallback.
- **`governance`** — the policy boundary: metric RBAC, column masking, RLS,
  k-anonymity, audit, per-tenant spend ledger, threat-model-as-code. `governance.Query`
  is the one path all governed reads go through.
- **`mcp`** — the MCP server (thick tools, never `run_sql`); real OIDC/JWT + RFC
  8693 on-behalf-of token exchange.
- **`agenttools`** — the single source of truth for agent-callable capabilities
  (describe_warehouse/list_metrics/get_dimensions/query_metric/health_check).
  Both the MCP server and the copilot delegate here so internal and external
  agents behave identically.
- **`copilot`** — an agent-go agent wrapping `agenttools`; `Run` and a streaming
  `Stream`. Used by `di copilot` and the web console.
- **`runtime`** — the HTTP server: stable versioned `/v1` API (`v1.go`) + the
  control-plane API + the embedded web console (`runtime/ui`, htmx/Alpine/GSAP/
  Tailwind, all `go:embed`-ed; no Node build).
- **`config`** — one `config.yaml` boots the `di serve` daemon (model/sources/
  warehouse/governance/auth, `${ENV}` expanded).
- Supporting: `modelgen` (introspect → generate a model draft), `reconcile`
  (cross-source conflict checks + LLM triage), `connectors` (generic
  mysql/mongo/redis/s3/kafka/csv/CRM adapters + webhook + CDC), `flow`
  (config-driven saga), `writeback` (propose→approve→commit→rollback), `rollout`
  (version registry + canary + auto-rollback), `nleval`, `obs` (OpenTelemetry),
  `cache`.

`cmd/di/main.go` is a Cobra command tree; each leaf wraps a `run*` handler that
parses its own flags (`DisableFlagParsing`), so adding a command means adding a
`run*` func + a `leaf(...)` line.

## The one rule that governs everything: stay domain-neutral

The platform binary contains **no customer or business logic**. Connectors are
generic adapters keyed by type+config; flows are config-driven primitives; the
semantic model, sources, governance policy, conflict checks, and threat model are
**data files** under `examples/` and `models/`. `examples/meridian/` (retail) and
`examples/fitness/` (a Solidcore-style studio) are *example integrations* proving
the engine is neutral — never hard-code a customer into Go. Generic, reusable
capabilities get pushed down into the libraries (`semantic-go`, `cortexdb`).

## Workspace & releases

A parent `go.work` includes `semantic-go`, `cortexdb`, `agent-go`, `eval-go`, so
local edits to those libs take effect immediately. For external builds the
published versions are pinned in `go.mod` — after changing a lib, tag/publish it
(e.g. `semantic-go` v0.1.x, `cortexdb` v2.x) and bump the pin. Verify external
resolution with `GOWORK=off go build ./...`.

The course this implements (semantic layer + Text-to-SQL + MCP, 23 modules) and
per-module status live in `docs/course-notes.md`; the full design in `docs/DESIGN.md`.
