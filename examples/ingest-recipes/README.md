# Ingest recipes — three ways data actually arrives

Row-level checks (types, primary keys, foreign keys) are not enough. The costly
failures are **aggregate-level**: a file imported twice, an amount column that
switched from yuan to cents, a store that silently stopped reporting. Every row
is valid; the totals are wrong.

These recipes land data through a staging table and a validation gate, so bad
batches stop before they reach the governed warehouse.

| Tier | Source | Recipe | Command it drives |
|---|---|---|---|
| 1 | Exported files (CSV/Excel, often non-English headers) | `load.sh` | `di ingest` |
| 2 | A live operational database | `cdc_runbook.sh`, `action_sync.sh` | `di sync` (watermark CDC) |
| 3 | A SaaS platform pushing orders | `webhook_runbook.sh` | `di webhook-ingest` |

## Shape

```
source → staging (raw, all TEXT) → transform + gate → governed warehouse
```

- `warehouse.sql` — the target contract: typed columns, primary keys, foreign keys.
- `transform.sql` / `transform_cdc.sql` / `transform_orders.sql` — the per-customer
  part: map their column names onto the contract, cast, de-duplicate, enforce
  foreign keys, mask PII. This is the only file a new customer needs.
- `export_source.sh` — produces a source file that mirrors what a POS or ERP
  export looks like, including a few dirty rows, so the gate can be verified.

## Run

```sh
export DI_BIN=../../di          # or wherever the di binary lives
./load.sh                       # tier 1: file → staging → gate → warehouse
./cdc_runbook.sh                # tier 2: watermark-incremental from a live DB
./webhook_runbook.sh            # tier 3: signed order pushes
```

Each script prints staging vs warehouse row counts, so rejected and de-duplicated
rows are visible rather than silent.

## Where the aggregate-level gate lives

These recipes stop bad *rows*. Catching a bad *batch* — "revenue jumped 99× but
the row count did not move" — needs a before/after comparison of the whole load.
That gate is implemented in
[`di-server`](https://github.com/liliang-cn/di-server)
as branch-diff: copy the affected tables into a branch, load there, diff the
aggregates against production, and promote only after a human looks.
