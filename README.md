# DataIntelligence

[![CI](https://github.com/liliang-cn/dataintelligence/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/dataintelligence/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/dataintelligence.svg)](https://pkg.go.dev/github.com/liliang-cn/dataintelligence)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/dataintelligence)](https://goreportcard.com/report/github.com/liliang-cn/dataintelligence)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**A governed semantic layer + MCP gateway that makes your data warehouse safe for AI agents.**

Point an LLM agent at a raw warehouse and it will, sooner or later, invent a join,
pick the wrong grain, multiply a total through a fan-out, choose one of three
"revenue" definitions at random, and return a confident wrong number that runs
clean. A crash is a gift; a silent wrong answer is the real problem.

DataIntelligence puts a layer between the agent and the warehouse that resolves
meaning, compiles fan-out/chasm-safe SQL, enforces governance on every hop, and
exposes only governed tools over [MCP](https://modelcontextprotocol.io). Agents
ask for a *metric by dimensions* — never raw SQL.

It is domain-neutral: the engine knows nothing about your business. Your model,
sources, and policies are config. `examples/meridian/` is one example integration.

---

## What it prevents

The five failure modes of naive text-to-SQL, blocked structurally — not by prompting:

1. **Wrong join** — relationships are declared once in a join graph; the compiler only traverses them.
2. **Wrong grain** — every measure is pinned to its grain.
3. **Fan-out / chasm** — each measure aggregates in its own CTE, then joins. Inflation is impossible by construction.
4. **Ambiguous metric** — one metric, one definition; synonyms route to it; RBAC gates who can resolve it.
5. **Silent wrong answer** — every metric reconciles to a control query in CI, and answers are graded against a labeled set.

## Quickstart (5 minutes)

### Try it — seeded warehouse + service, one command

```bash
cd deploy/platform
docker compose up --build          # Postgres (seeded) + the service
curl localhost:41900/v1/healthz    # {"status":"ok"}

# a governed query, sliced by region
curl -s -X POST localhost:41900/v1/query -H 'X-DI-Role: finance' \
  -d '{"metrics":["total_revenue"],"group_by":["store_region"]}'
```

### Use it on YOUR warehouse

```bash
go install github.com/liliang-cn/dataintelligence/cmd/di@latest

# 1. generate a semantic-model draft from your live schema (heuristic; add LLM_* env to refine)
di model gen -dsn "postgres://user:pass@host:5432/db?sslmode=disable" -out model.yaml
#   -- introspected 9 table(s)
#   -- mode: heuristic · 7 entities, 6 joins, 18 dimensions, 11 metrics · 0 lint note(s)

di model lint -model model.yaml    # review it, then serve

# 2. serve it (REST /v1 + MCP)
DI_DSN="postgres://user:pass@host:5432/db?sslmode=disable" di serve -model model.yaml

# 3. ask in natural language, governed end to end
curl -s -X POST localhost:41900/v1/ask -H 'X-DI-Role: finance' \
  -d '{"question":"total revenue by region"}'
```

### Connect an agent (MCP)

The MCP server exposes `list_metrics`, `get_dimensions`, `query_metric` — and
deliberately **no `run_sql`**. Point any MCP client at it. For Claude Desktop:

```json
{
  "mcpServers": {
    "dataintelligence": {
      "command": "di",
      "args": ["mcp"],
      "env": { "DI_DSN": "postgres://user:pass@host:5432/db?sslmode=disable" }
    }
  }
}
```

## Architecture

```
  Agent / app / CLI
        │  natural language
        ▼
  GROUNDING        NL → retrieve metrics (BM25 ⊕ dense ⊕ cross-encoder rerank),
                   few-shot, disambiguate → a typed semantic query (never raw SQL)
        ▼
  SEMANTIC LAYER   entities · dimensions · metrics · join graph
   (semantic-go)   COMPILER: aggregate-to-grain-in-CTE → join → dialect SQL
        ▼                    (Postgres · Snowflake · Databricks)
  WAREHOUSE        database/sql + cost ceiling + row cap + timeout
        ▼
     your warehouse

  GOVERNANCE on every hop:  RBAC · row-level security · column masking ·
                            k-anonymity · per-user identity (OIDC + RFC 8693 OBO)
  OBSERVABILITY:            OpenTelemetry span tree + cost, eval gates, audit
  EXPOSED VIA:              REST /v1  and  MCP (governed tools only)
```

Build order is load-bearing: **meaning first, transport last.**

## What's in the box

| Area | Capability | Command |
|---|---|---|
| Onboarding | introspect a warehouse → generate a model draft | `di model gen` |
| Query | governed semantic query → fan-out-safe SQL | `di query`, `POST /v1/query` |
| NL | ground a question, optionally answer | `di ask`, `POST /v1/ground` `/v1/ask` |
| Dialects | same model → Postgres / MySQL / SQLite / SQL Server / Snowflake / Databricks SQL | `di explain -dialect` |
| Governance | RBAC, masking, RLS, k-anon, threat-model-as-code | `di threats` |
| Identity | real OIDC/JWT + on-behalf-of to the warehouse | `di obo`, `di pentest` |
| Evaluation | accuracy gate vs control SQL + LLM judge | `di nleval` |
| Write-back | NL → typed proposal → approve → commit → rollback | `di propose/approve/revert` |
| Rollout | model version registry, canary, auto-rollback | `di rollout` |
| Service | config-driven daemon, REST /v1 + MCP | `di serve` |

See [`docs/DESIGN.md`](docs/DESIGN.md) for the full design and [`deploy/`](deploy/) for Docker / Compose / Helm.

## Several databases, one process

A semantic model is scoped to one warehouse, so "which model" and "which
warehouse" are the same question — a database. Declare as many as you have:

```yaml
databases:
  - id: conglomerate
    model: models/conglomerate.yaml
    dsn: postgres://…/conglomerate
  - id: shop
    model: models/shop.yaml
    dsn: mysql://…/shop
```

Each gets its own MCP endpoint and is selectable over REST:

```
MCP   :41910/db/conglomerate          REST  X-DI-Database: conglomerate
```

Engines open **lazily**: booting costs no connections, and one unreachable
warehouse fails that database's requests instead of keeping the healthy ones
offline. An unknown name is an error that lists what is configured, so a typo
never looks like an outage.

Selection sits with the URL a client connects to, or a header it sets — never a
tool argument. A model must not be able to wander between a company's databases
mid-conversation; which database to look at is the product's decision, made once.

The single-database form (top-level `model:` + `warehouse.dsn:`) still works and
becomes one database named `default`.

### Modelled or not — the gate that decides how a database can be read

Leave `model:` out and the database is **unmodelled**. That is the day-one
state: connected, nothing modelled yet, and someone needs to look around before
they can declare a single metric.

| | modelled | unmodelled |
|---|---|---|
| MCP endpoint | yes — `list_metrics` / `get_dimensions` / `query_metric` | **none** |
| `POST /v1/query` | yes | no — there are no metrics |
| `POST /v1/sql` | refused unless `allow_raw_sql: true` | yes, read-only |

The refusal is enforced here, in the engine, not in whichever client is asking.
A product should *also* decline to hand its model a raw-SQL tool — but that is
defence in depth on top of this, not the load-bearing part.

An unmodelled database gets no MCP endpoint at all rather than one with an empty
metric list: to an agent, "no metrics" reads as *this business has no revenue*,
not *nobody has modelled this yet*.

`POST /v1/sql` is deliberately not an MCP tool and will not become one. It exists
for a trusted first-party product exploring a database before there is a semantic
layer to go through. Every call is audited with the statement and row count.

## Warehouses

The backend and the SQL dialect are both chosen from the DSN scheme, together —
compiling Postgres SQL for MySQL is how "revenue by month" becomes a syntax
error, or worse, a number bucketed the wrong way.

| DSN | Engine | Governance that does **not** apply |
|---|---|---|
| `postgres://` | PostgreSQL | — full support, including RLS and on-behalf-of |
| `mysql://` `mariadb://` | MySQL 8 / MariaDB | no row-level security (the engine has none) |
| `sqlite://` | SQLite file | no row-level security |
| `sqlserver://` `mssql://` | SQL Server 2017+ | RLS not wired up (needs SESSION_CONTEXT, not GUCs); no read-only transaction flag |
| `duckdb:` | DuckDB | build with `-tags duckdb`; no cost pre-flight |

Where a control cannot be enforced, DI **refuses to start** rather than accept a
setting that appears to apply. Point `DI_DB_APP_ROLE` at MySQL and you get an
error naming the reason, not a silently ungoverned connection. The semantic
layer's own governance — metric RBAC, column masking, k-anonymity — is enforced
in Go and applies on every engine.

Two differences worth knowing because they are not visible in a result set:

- **MySQL has no `DATE_TRUNC`** in any version. Grains are emulated with date
  arithmetic that returns a DATE, and weeks start Monday to match Postgres
  rather than following MySQL's Sunday-default `WEEK()`.
- **SQLite has no date type.** A timestamp column is declared `TEXT`, so
  `di model gen` samples the actual values to decide whether a column is a time
  dimension. A column named `order_date` holding `"Q3"` stays categorical.

## Scope: an engine, not an end-user product

DataIntelligence is the **governance engine and data plane**. Its own surfaces are
built for the people who run it:

- `di ask` / `chat` / `agent` / `copilot` — **CLI**, for trying a model, debugging
  grounding, and scripting. Not a product chat window.
- `di serve`'s `/ui` — an **operator console**: model, eval runs, traces,
  write-back approvals, rollout. Not a business-user dashboard.
- `/v1` + MCP — the **integration surface**. This is how products consume it.

Anything user-facing — an executive chat UI, onboarding wizards, desktop
packaging, dashboards — belongs in a product **on top of** DI, talking to `/v1`
or MCP. One such product is
[`di-server`](https://github.com/liliang-cn/di-server),
which uses DI for governed metrics and adds what DI deliberately does not do:
direct (unmodeled) SQL exploration, pre-ingestion branch diffing, and an
executive-facing UI.

**DI does not ship an end-user product.** Keeping that line means the engine stays
domain-neutral and embeddable, instead of growing a second, weaker BI tool inside it.

## Status

The semantic + grounding + governance + MCP spine is production-grade and
measured: the NL eval gate runs a labeled set against hand-written control
queries with a per-category accuracy floor, and every metric reconciles in CI.
Built on three reusable libraries: [`semantic-go`](https://github.com/liliang-cn/semantic-go)
(the layer + compiler), [`cortexdb`](https://github.com/liliang-cn/cortexdb)
(retrieval), and `agent-go` / `eval-go`.

## Support & consulting

DataIntelligence is free and open source (Apache-2.0) — use it, fork it, ship it.

If you want help standing it up against your warehouse, modeling contested
metrics, wiring it into your agent stack, or hardening governance for production,
that's what I do for a living. Open an issue, or reach out: **ll_faw@hotmail.com**.

## License

[Apache-2.0](LICENSE).
