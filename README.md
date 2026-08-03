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
| Discovery | what is actually in the customer's database | `di survey` |
| Onboarding | introspect a warehouse → generate a model draft, with ratios | `di model gen` |
| Query | governed semantic query → fan-out-safe SQL | `di query`, `POST /v1/query` |
| NL | ground a question, optionally answer | `di ask`, `POST /v1/ground` `/v1/ask` |
| Dialects | same model → Postgres / MySQL / SQLite / SQL Server / Snowflake / Databricks SQL | `di explain -dialect` |
| Governance | RBAC, masking, RLS, k-anon, threat-model-as-code | `di threats` |
| Identity | real OIDC/JWT + on-behalf-of to the warehouse | `di obo`, `di pentest` |
| Evaluation | accuracy gate vs control SQL + LLM judge | `di nleval` |
| Handover | delivery report: what was modelled, and what proves it | `di report` |
| Day 2 | runbook + CI gate for the team that inherits it | `di handover` |
| Day 2 | has anything changed underneath the model? | `di drift` |
| Day 2 | who uses this, and what nobody ever asks for | `di adoption` |
| Feedback | what the product could not do, across engagements | `di delta` |
| Write-back | NL → typed proposal → approve → commit → rollback | `di propose/approve/revert` |
| Rollout | model version registry, canary, auto-rollback | `di rollout` |
| Service | config-driven daemon, REST /v1 + MCP | `di serve` |

See [`docs/DESIGN.md`](docs/DESIGN.md) for the full design, [`deploy/`](deploy/) for
Docker / Compose / Helm, and [`docs/FDE-ROADMAP.md`](docs/FDE-ROADMAP.md) for where
this is going: the scaffolding a forward-deployed engineer uses to run an
engagement end to end — survey, model, prove, deliver, hand over, and feed the
gaps back into the core.

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

### Registering a database at runtime

A product's setup wizard needs a user to type connection details and be querying
a minute later — not edit YAML and restart a service. Set `databases_file:` and
databases can be registered over the API and survive a restart:

```bash
curl -XPOST     :41900/v1/databases -d '{"id":"shop","dsn":"mysql://…/shop"}'
curl -XDELETE   :41900/v1/databases/shop
```

The DSN is opened before it is saved, so a bad connection string fails while the
person who typed it is still looking at the form.

One more call turns a connected database into a governed one:

```bash
curl -XPOST :41900/v1/databases/shop/model
# {"metrics":8,"dimensions":5,"tables":3,"path":"…/models/shop.yaml"}
```

It introspects the live schema, drafts a model, and re-registers the database as
governed — which also means `/v1/sql` stops working on it. That is the point. The
draft is heuristic and the response reports counts rather than implying the
modelling is finished: someone who knows the business still has to check what
"revenue" means.

This is **off unless `databases_file` is set**. An endpoint that opens a
connection string the caller supplies is not something to have on by accident on
a networked service. Config-file databases cannot be removed through the API —
an operator's YAML is intent, and the entry would reappear on the next restart
anyway. Nor can the default be removed while others exist: unqualified requests
would silently move to a different company's data.

With `databases_file` set and nothing declared, DI starts with **zero**
databases. That is the honest state of a product before its user has connected
anything, so `/v1` is up and says so rather than refusing to boot.

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

## The pre-ingestion gate

Row-level checks — types, keys, foreign keys — do not catch the expensive
failures, because those are aggregate-level. A file imported twice. An amount
column that switched from yuan to cents. A store that quietly stopped reporting.
Every row is valid, the totals are wrong, and nothing errors.

So a batch lands in a **branch** first — a `br_<name>` schema holding a copy of
the affected tables — and is compared with production before anyone commits to it:

```bash
curl -XPOST  :41900/v1/branch          -d '{"name":"jan","tables":["orders"]}'
# … load the batch into br_jan …
curl         ':41900/v1/branch/diff?name=jan'
curl -XPOST  :41900/v1/branch/promote  -d '{"name":"jan"}'   # or /discard
```

The diff reports row counts and the total of every measure, per table, and names
what looks wrong:

| batch | what the diff says |
|---|---|
| ordinary +60 rows | no signals |
| same file loaded twice | `row count exactly doubled — a duplicate load` |
| yuan became cents | `row count barely moved but amount rose +9900.0% — a unit or definition change` |
| a store stopped reporting | `branch has 1000 fewer rows than production` |

Key columns are excluded from the totals: summing a climbing primary key makes
every ordinary load look like a 66744% change, and a gate that fires every time
is one people learn to click past.

Only the diff is a read. Promote replaces production's data — in one
transaction, so a failure partway cannot leave some tables swapped, one emptied,
and no way to tell which — and it is never exposed as an agent-callable tool.

PostgreSQL only for now: in MySQL a schema *is* a database and SQLite is a single
file, so each needs its own mechanism. Calling it elsewhere says so rather than
doing something half-right.

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

## One engagement, one file

The rest of this README is organised around a database. An engineer standing up
a customer is organised around a customer: several databases, a model for each
one that has been modelled, the reconciliation set that proves it, the labelled
questions, the report — and what had to be worked around to make this fit.

```yaml
# engagement.yaml
customer: Acme Retail
databases:
  - id: erp
    dsn: ${ACME_ERP_DSN}
    model: models/erp.yaml        # recon defaults to models/erp.recon.yaml
  - id: pos
    dsn: ${ACME_POS_DSN}          # not modelled yet — direct SQL only
evalset: models/questions.yaml
deliver:
  report: out/delivery.md
```

Then the commands take no flags at all:

```bash
di survey     # week one: what is actually in there
di eval       # every metric vs its control query
di report     # the handover document
```

Paths resolve against the file rather than the working directory, and a
`${VAR}` the environment does not define is reported by name — an unset DSN
otherwise surfaces as "database erp needs a dsn", which is true and points at
the wrong thing.

**[`examples/engagement/`](examples/engagement/) is one customer start to
finish**, with the artefacts it produced and a `warehouse.sql` you can build so
the figures in it are the figures you get. It is also where the point of all
this is easiest to see: revenue per square metre comes out as 16,191.71 through
the semantic layer and 112.44 from the obvious hand-written join, because
joining floor area to sales repeats each shop's area once per sales row. The
naive query runs clean and is wrong by 144×.

[`docs/FDE-ROADMAP.md`](docs/FDE-ROADMAP.md) is where this is going.

## The site survey

Week one is spent discovering that the schema diagram is out of date, one feed
stopped six months ago, and a third of the foreign keys point at rows that do
not exist. None of that is in a schema dump, and all of it decides what can be
modelled.

```bash
di survey -out survey.md
```

```
- 4 tables, 4,540 rows in total
- 1 table(s) are empty
- 1 table(s) have no primary key — de-duplication has nothing to key on
- 1 foreign key(s) are not honoured by the data — 37 orphan row(s);
  joins on them will drop rows

**legacy_feed** — 500 rows
- synced_at stops at 2025-11-01 — has this feed stopped?

**orders** — 4,037 rows
- channel has one value in every row — a constant, not a dimension
- note is entirely null — nothing can be modelled on it
```

Every figure comes from a query; nothing is inferred from names. It runs against
a customer's production database on day one, so the distinct-value probe is
capped, the referential-integrity checks can be skipped, and a column that
cannot be profiled is skipped rather than aborting the report.

The finding worth the whole exercise is the stale feed. A source that stopped
sending leaves every row valid and the totals quietly not growing — nothing
errors, and a model built on it looks fine until someone asks why last quarter
is flat.

## What the draft proposes

`di model gen` produces sums, and — where one table holds both sides of the
question — the ratios people actually ask for:

```
sale_gross_margin             = (sale_revenue_sum - sale_cogs_amount_sum) / sale_revenue_sum
sale_revenue_per_transaction  = sale_revenue_sum / sale_transactions_sum
sale_revenue_per_unit         = sale_revenue_sum / sale_units_sum
```

Nobody asks for "sum of revenue and sum of cost"; they ask for margin. A draft
made only of sums stops just short of the questions, and reads as unfinished
because it is.

Every proposal is **non-additive**, which is the part that matters: a margin
summed across regions, or averaged from monthly averages, is the classic wrong
number, and declaring it lets the compiler refuse.

Two limits, both deliberate:

- **Same table only.** Both operands share a grain, so the ratio is arithmetic
  rather than a join whose correctness depends on cardinality. A cross-table
  ratio — loss in one fact table over revenue in another — is a decision about
  which pairing means something, and guessing produces plausible metrics nobody
  asked for. Those stay hand-written.
- **Matched on names.** No column announces that it is a cost. The meaning is in
  the name and nowhere else, so this is explicitly a draft: every description
  says to confirm it is the margin the business means.

## Day 2 — the day after you leave

The most common way this work dies is not a bad model. It is that nothing tells
the team when the thing has quietly stopped being true: a column gets renamed, a
feed stops, someone changes a definition that turned out to be load-bearing. The
answers keep coming out clean and wrong.

```bash
di handover   # writes RUNBOOK.md and a CI workflow beside the engagement
di drift      # exits non-zero when something needs attention
di adoption   # who used it, and which metrics nobody ever asks for
```

`di drift` checks the three failures that produce clean-looking wrong answers
instead of errors:

```
DRIFT — 1 column(s) gone, 1 feed(s) stopped, 1 metric(s) no longer match their control
  stores.city is gone, used by store_city — every query grouping on it now fails
  legacy_synced_at stops at 2025-12-01 (245 days ago) — totals over it are quietly no longer growing
  order_count = 2001, its control says 2000 (anchored to customer-report)
```

Each line says what to do about it. A monitor that reports only "drift detected"
hands the reader a search problem at the worst possible moment.

`di adoption` reads the audit trail back. The valuable half is the negative:

```
## Modelled and never asked for (2)
Each of these was defined, argued about, and checked. In 30 days nobody has
asked for one. That is either work that should not have been done, or a
metric nobody knows exists — worth finding out which before modelling more.
```

## Feeding the gaps back

An engineer who finishes and leaves is a consultant. What makes the next
delivery cheaper is recording what the product could not do, and counting it:

```yaml
# in engagement.yaml
delta:
  - kind: missing-feature
    what: the customer's fiscal year starts in April; the compiler only knows calendar years
    workaround: a separate fiscal_period dimension maintained by hand
```

```bash
di delta -root ~/engagements
```

```
### the customer's fiscal year starts in April × 3
- hit by  Acme, Beta, Gamma
- worked around with `a hand-maintained fiscal_period dimension`, `copied Acme's approach again`
```

Once is a workaround. Three times is a missing feature, and the third person to
hand-roll it is the expensive one. Grouping is by normalised text and nothing
cleverer: a clustering that guessed would merge gaps that are not the same, and
an under-counted gap is visible next time somebody reads the list while a wrongly
merged one becomes a feature request for something nobody needed.

## The handover document

Modelling someone's warehouse is otherwise unfalsifiable work: you deliver a YAML
file and a dashboard, and nobody — including you — can say whether the numbers
are right. Two gates already answer that; `di report` renders them as one
document for the person paying for it.

```bash
di report -model models/shop.yaml -dsn "$DSN" -database "Acme" -out delivery.md
```

```
PARTIAL — 6/10 metrics reconcile, 4 have no control query

## Not verified (4)
- revenue_rolling_3   - revenue_delta   - revenue_cumulative   - revenue_ytd

## Metric reconciliation — 6/6
| net_revenue | 6126603.38 | 6126603.38 | ✓ |
  The contested one — revenue net of refunds. Two tables at different grains,
  which is exactly where a hand-written join would inflate the total.

## Natural-language accuracy — 100% (37/37)
```

The gaps come first, by name. A report that shows only what passed is marketing,
and the reader finds the holes later anyway — at a worse moment. `PARTIAL` and
`VERIFIED` are different words for a reason: six checks over ten metrics is not
the same claim as six over six.

Reconciliation cases are **data**, beside the model — and each one records where
its expected figure came from:

```yaml
# models/shop.recon.yaml
cases:
  - metric: net_revenue
    control: SELECT (SELECT sum(quantity*unit_price) FROM order_items)
                  - (SELECT sum(refund_amount) FROM refunds)
    source: customer-report      # their Q2 board pack, page 3
    note: Revenue net of refunds — two tables at different grains.

  - metric: order_qty_sum
    control: SELECT sum(quantity) FROM order_items
    source: engineer             # derived from the schema — no external anchor
```

`source` is the difference between verification and theatre. A control query
written by whoever wrote the metric, from the same misunderstanding, agrees with
it — and agreement is not correctness. So a report where nothing is anchored
says **SELF-CONSISTENT**, not VERIFIED:

```
VERIFIED         5/5 metrics reconcile, 2 of 5 anchored to customer figures
SELF-CONSISTENT  5/5 metrics reconcile, none is anchored to a customer figure
PARTIAL          2/5 metrics reconcile, 3 have no control query, 1 of 2 anchored
```

Coverage and anchoring are separate gaps and neither hides the other: a reader
told only one of them has been misled by omission.

They used to be Go code listing one example's metrics, which meant standing up a
new customer produced a green check for metrics that customer does not have.

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
