# DataIntelligence — Quickstart

A governed semantic layer + MCP gateway that makes a warehouse safe for AI agents.
The agent decides **what** to ask (a metric × dimensions × filters); a deterministic
layer guarantees the answer is **correct** (fan-out/chasm-safe SQL, RBAC, RLS, masking).

Three ways to start, fastest first. Every command below is copy-paste runnable.

---

## Path A — see it work in 1 command (seeded)

Boots a seeded Postgres **and** the service together. Nothing to configure.

```bash
cd deploy/platform
docker compose up --build
# → REST /v1 + MCP + web console, against a pre-seeded retail warehouse (Meridian)

curl localhost:41900/v1/healthz                       # {"status":"ok"}

# a governed query, sliced by region, as the finance role
curl -s -X POST localhost:41900/v1/query -H 'X-DI-Role: finance' \
  -d '{"metrics":["net_revenue"],"group_by":["store_region"]}'
```

Open the console at **http://localhost:41900/ui** — query playground, model browser,
NL-accuracy history, write-back approvals, traces, copilot.

---

## Path B — build from source and run the gates

The CLI **is** the end-to-end test harness. This is the fastest way to trust the engine.

```bash
# 1. build (pure Go, CGO-free)
go build -o /tmp/di ./cmd/di

# 2. bring up a local warehouse (Postgres on :39632, schema + seed auto-applied)
cd deploy && docker compose up -d && cd ..
export DI_DSN='postgres://meridian:meridian@localhost:39632/meridian?sslmode=disable'

# 3. run the five gates — these must all stay green
/tmp/di eval          # reconciliation: every metric == a hand-written control query   → 5/5
/tmp/di model lint    # metadata gate: every metric has a description                   → 0 issues
/tmp/di threats       # threat-model-as-code (examples/meridian/threats.yaml)           → 10/10
/tmp/di pentest       # MCP security regression: forged-token battery                   → 9/9
/tmp/di nleval        # NL accuracy gate over models/nl_evalset.yaml (needs LLM, see D) → 37/37
```

Ad-hoc queries and NL, all governed:

```bash
/tmp/di query -metrics net_revenue -by store_region -role finance
/tmp/di explain -metrics total_revenue -by store_region -dialect snowflake   # compile only, no exec
/tmp/di ask "total revenue by region"        # NL → semantic query → SQL (needs LLM for best results)
```

Try governance: ask for `net_revenue` as `analyst` and it is **refused before any SQL runs**
(RBAC); group by `customer_email` and the column comes back masked.

---

## Path C — use it on YOUR warehouse

The platform binary contains **no business logic**. Your model, sources, and policies
are config — point it at any Postgres / Snowflake / Databricks / DuckDB.

```bash
go install github.com/liliang-cn/dataintelligence/cmd/di@latest

export DI_DSN='postgres://user:pass@host:5432/db?sslmode=disable'

# 1. introspect the live schema → a semantic-model draft (heuristic; add LLM_* to refine)
di model gen -dsn "$DI_DSN" -out model.yaml
di model lint -model model.yaml          # review the draft, fix descriptions, re-lint

# 2. reconcile a couple of metrics against control SQL you trust, then serve
di eval -model model.yaml

# 3. serve: REST /v1 + MCP + web console, from one process
di serve -model model.yaml               # or: di serve -config config.yaml
```

One config file boots the whole daemon (see `config/config.example.yaml`):

```yaml
model: model.yaml
warehouse: { dsn: "${DI_DSN}", max_rows: 10000, timeout_secs: 30 }
auth:   { }                              # omit oidc → open (dev); add it → bearer token required
server: { rest_addr: ":41900", mcp_addr: ":41910" }
```

### REST surface (`/v1`)

| Method & path | What |
|---|---|
| `GET  /v1/healthz` · `/v1/readyz` | liveness / readiness |
| `GET  /v1/metrics` | list governed metrics |
| `GET  /v1/metrics/{name}/dimensions` | dimensions valid for a metric |
| `POST /v1/query` | typed semantic query → governed rows + compiled SQL |
| `POST /v1/ground` | NL → semantic query (no execution) |
| `POST /v1/ask` | NL → answer, governed end to end |

Role travels in the `X-DI-Role` header (dev). With `auth.oidc` set, identity comes
from a bearer token and propagates to the warehouse via RFC 8693 on-behalf-of (RLS).

### Connect an AI agent (MCP)

The MCP server exposes `list_metrics`, `get_dimensions`, `query_metric` — and
deliberately **no `run_sql`**. Point any MCP client at it. Claude Desktop:

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

---

## D — turn on the AI features (any OpenAI-compatible endpoint, incl. local)

`nleval`, `ask`/`chat`, `copilot`, `reconcile -ai`, and `model gen -llm` use an LLM
only when these are set; otherwise they fall back to deterministic paths
(keyword grounding, the 10 `needs_llm` eval cases are skipped, etc.).

```bash
# generation LLM
export LLM_BASE_URL=...  LLM_API_KEY=...  LLM_MODEL=...
# dense-retrieval embeddings (optional; enables the vector leg of grounding)
export DI_EMBED_BASE_URL=...  DI_EMBED_API_KEY=...  DI_EMBED_MODEL=...
```

**Fully local with Ollama** (no cloud, offline) — verified working:

```bash
# ollama pull qwen3.5           # generation
# ollama pull embeddinggemma    # embeddings
export LLM_BASE_URL=http://localhost:11434/v1  LLM_API_KEY=ollama  LLM_MODEL=qwen3.5:latest
export DI_EMBED_BASE_URL=http://localhost:11434/v1  DI_EMBED_API_KEY=ollama  DI_EMBED_MODEL=embeddinggemma:latest

di nleval        # NL accuracy gate, run entirely on local models
di copilot "which store region has the highest net revenue, and recommend one governed fix"
```

---

## The two example integrations

Proof the engine is domain-neutral — same binary, zero code changes, only data + `model.yaml` differ.

- **`examples/meridian/`** — retail. The default; drives all the gates above.
- **`examples/fitness/`** — a boutique-pilates studio (`examples/fitness/README.md`).
  Demonstrates chasm-safe ratios across grains (`fill_rate`, `no_show_rate`), finance-only
  `revenue`, masked `member_email`, region RLS:

  ```bash
  createdb fitness
  psql -d fitness -f examples/fitness/schema/01_schema.sql
  psql -d fitness -f examples/fitness/schema/02_seed.sql
  export FIT="postgres://USER:PASS@HOST:5432/fitness?sslmode=disable"
  di query -model examples/fitness/model.yaml -dsn "$FIT" -metrics fill_rate -by studio_city
  ```

---

## Who the web console (`/ui`) is for

The operator / integrator surface — not an end-user BI tool. It lets whoever is wiring
DataIntelligence into a warehouse browse the model, watch NL-accuracy over time, approve
write-backs, inspect traces, and demo the guarantees live (flip the role dropdown and
watch RBAC refuse a metric; expand the compiled fan-out-safe SQL). External agents never
touch it — they go through MCP (`:41910`) or REST (`/v1`).

---

## Where to go next

- `docs/DESIGN.md` — the full design.
- `docs/course-notes.md` — the 23-module course this implements, with per-module status.
- `docs/RUNBOOK.md` — operating it.
- `deploy/` — Docker / Compose / Helm.
