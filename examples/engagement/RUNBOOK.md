# Runbook — Conglomerate Retail

Written for whoever inherits this and was not in the meetings.

Delivered by liliang, starting 2026-08-03.

## What you have

1 database(s), 1 of them modelled.

| Database | Mode | What that means |
|---|---|---|
| `warehouse` | governed | questions are answered from declared metrics; raw SQL is refused |

A **governed** database answers only in terms of metrics somebody defined and
checked. That is the point of it: the same question gives the same number to
everyone, and "revenue" cannot quietly mean three things. An **unmodelled**
one is still being explored — useful, but nothing vouches for the answers.

## The three commands

```bash
di drift      # has anything changed underneath us?   ← run this on a schedule
di eval       # does every metric still match its control query?
di report     # regenerate the delivery document
```

Run them from the directory holding `engagement.yaml`. None of them takes
arguments: the engagement file is the configuration, so what a colleague runs
is what you ran.

## When someone says a number looks wrong

1. `di eval` — if a metric stopped matching its control query, that is the answer.
2. `di drift` — a column renamed, or a feed that stopped, will show here and
   nowhere else. Both produce clean-looking wrong answers rather than errors.
3. Ask which number they expected and where it comes from. If it is a figure
   your business publishes, add it as a control query with `source:` naming it
   — see below. That converts an argument into a check that runs forever.

## Adding or changing a metric

A metric lives in the semantic model; the check that it is right lives beside it.

- `warehouse` → model `models/warehouse.yaml`, checks `models/warehouse.recon.yaml`

```yaml
# in the .recon.yaml, beside the model
cases:
  - metric: net_revenue
    control: SELECT sum(amount) - sum(refunds) FROM …
    source: customer-report      # WHERE the expected number comes from
    note: the Q2 board pack, page 3
```

`source` is the part people skip and the part that matters. A control query
written by whoever wrote the metric proves only that the model agrees with
itself. A control anchored to a number your business already publishes proves
it is right. `di report` counts the two separately and says so.

## Run the checks automatically

`di drift` exits non-zero when something needs attention, so it works as a
scheduled job or a CI step. A gate nobody runs is a document; see
`.github/workflows/di-gate.yml`, generated beside this file.

## What is yours to decide, not the tool's

- **What a metric means.** The tool guarantees the same definition is applied
  everywhere. It cannot tell you whether revenue should be net of refunds.
- **Who may see what.** Roles gate metrics — this delivery uses ceo, finance, store_manager. Adding a person to a
  role is a business decision.
- **Whether a feed is allowed to stop.** `di drift` reports one that has. Only
  you know whether that is a broken pipeline or a shop that closed.

## Known rough edges

Things this delivery needed that the platform does not do natively. They
work, and they are the parts most likely to surprise you:

- **missing-feature** — cross-table ratios (shrinkage over revenue, revenue over floor area) are not proposed by the generator
  Handled by `written by hand into models/warehouse.yaml`.
- **data-issue** — every fact table stops at 2025-12-01 — eight months with no new data
  Handled by `none; the customer's IT need to say whether the load stopped or moved`.

## Where things are

| | |
|---|---|
| Engagement | `/Users/liliang/Things/AI/base/dataintelligence/examples/engagement/engagement.yaml` |
| Service | http://di.internal:41900 |
| Delivery report | `out/delivery.md` |
