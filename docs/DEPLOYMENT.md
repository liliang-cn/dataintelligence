# Deployment architecture

The fourth of the four artefacts an FDE hands over. The other three say what was
built and whether it is right; this one says where it runs and what it can reach.

## The request path

```
  ┌─────────────┐   NL question    ┌──────────────────────────────────────┐
  │  the person │ ───────────────► │ product layer  (di-server, Rust)     │
  │  asking     │ ◄─────────────── │  conversation · gating · UI · tokens │
  └─────────────┘   answer + the   │  ZERO database code                  │
                    SQL that ran   └──────────────┬───────────────────────┘
                                                  │ HTTP  X-DI-Database
                                                  │       X-DI-Role
                                   ┌──────────────▼───────────────────────┐
                                   │ core  (DataIntelligence, Go)         │
                                   │                                      │
   grounding ── NL → semantic query (BM25 ⊕ dense ⊕ rerank, LLM optional) │
        │                                                                 │
   governance ─ metric RBAC · column masking · RLS · k-anonymity          │
        │       · spend ledger · audit                                    │
        │                                                                 │
   semantic-go ─ compile to fanout/chasm-safe SQL                         │
        │                                                                 │
   warehouse ── timeout · row cap · EXPLAIN byte ceiling · SET LOCAL ROLE  │
                                   └──────────────┬───────────────────────┘
                                                  │
                    ┌─────────────────────────────┼──────────────────────┐
                    ▼                             ▼                      ▼
              Postgres / MySQL             SQL Server / SQLite        DuckDB
```

Two properties of this picture are load-bearing:

**The product layer has no database code.** Not "should not" — does not. It
speaks HTTP to core and nothing else. Every rule about what may be read lives on
one side of that line, so there is one place to audit and no second
implementation to drift.

**Nothing reaches the warehouse except through the compiler.** On a modelled
database the agent cannot emit SQL; it emits a typed semantic query and the
compiler produces the SQL. That is a property of the tool surface, not of a
prompt — there is no `run_sql` tool to talk it into using.

## Two modes, decided by whether a model exists

| | 直连 (unmodelled) | 治理 (modelled) |
|---|---|---|
| What the agent may do | read-only SQL | metrics only |
| Fan-out safety | none — it is raw SQL | guaranteed by construction |
| RBAC / masking / RLS | connection-level only | per metric, per column, per row |
| What the audit shows | the SQL that ran | the semantic query and the SQL |
| Use it for | week one, exploration | anything anyone reports on |

A database moves from the first to the second by acquiring a model. Nothing else
changes — same endpoint, same product, same audit table.

## Where things run

Everything can run inside the customer's network. Nothing in the request path
requires egress:

- **core** — one Go binary, CGO-free unless the DuckDB backend is built in. One
  `config.yaml`. Reads database credentials from the environment.
- **product** — one Rust binary, or the desktop build.
- **the warehouse** — the customer's, read-only user.
- **the model** — a text file in the customer's repository, reviewed like code.
- **the LLM** — optional and pluggable (`LLM_BASE_URL` / `LLM_API_KEY` /
  `LLM_MODEL`). Point it at a local deployment and nothing leaves the building.
  Without it, grounding falls back to a deterministic keyword matcher that asks
  rather than guesses.

The one caveat worth saying out loud to a customer: a small local model
fabricates coefficients when asked to write SQL — we have watched one emit
`SUM(amount) * 1.4`. That is an argument for the governed mode, not against
local deployment. In governed mode it physically cannot.

## Multi-customer

One deployment can serve several engagements. `config.yaml` names which:

```yaml
engagement: "客户A · 生产"
```

Every audit row is stamped with it, and everything that reads the trail —
`di adoption`, `di questions` — filters on it. An audit that cannot be scoped to
a customer cannot be shown to that customer, and that is the moment somebody
exports the whole table and hands over a second customer's questions with it.

## Data landing

APIs are never on the query path. A REST or OData service cannot take a pushed
down `GROUP BY`, cannot be cost-estimated with `EXPLAIN`, and has no role to
`SET` — so records land in the warehouse first and every governed read after
that is unchanged.

```
SAP OData ─┐
管家婆云   ─┼─► connectors (type: http) ─► ingest ─► warehouse ─► survey ─► model
SQL Server ┘     rate-limited, keyed, typed
```

See [`examples/erp/`](../examples/erp/) for the manifest and for the part
transport does not solve.

## What the customer has to provide

1. A read-only database user, and the network path to reach it.
2. The environment variables named in the delivery's `MANIFEST.md`.
3. A decision about who may see what. **Authorization does not come along with
   the data** — SAP's authorization objects, the ERP's role table, none of it
   arrives with the rows. It has to be declared again in `governance`. That is
   in scope, and it is not free.

## What runs after we leave

`di drift`, on a schedule, exiting non-zero. It checks that the tables and
columns the model depends on still exist, that no feed has stopped — including
one plant stopping while the others report, which a table-level check cannot see
— and that every metric still agrees with its control. `di handover` generates
both the runbook and the CI workflow that runs it, because a gate somebody has
to remember is a document.
