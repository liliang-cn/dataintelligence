# Landing an ERP

```bash
di source list   -manifest examples/erp/sources.yaml
di source read   -manifest examples/erp/sources.yaml sap_billing        # look before you load
di source ingest -manifest examples/erp/sources.yaml -dsn "$DSN" -table sap_billing sap_billing
di survey  -dsn "$DSN"                                                  # what actually arrived
di model gen -dsn "$DSN" -out model.yaml                                # a draft
```

Then the ordinary engagement flow: edit the draft, write control queries, `di eval`,
`di report`, `di handover`, `di drift`.

## Transport is the easy half

These systems will give you clean SQL that returns the wrong number. Every one
of these runs without error:

| | What it does to the number |
|---|---|
| `MANDT` — SAP's client column, on every table | test-client rows are counted with production |
| `SHKZG` — debit/credit indicator (`S`/`H`) | credits are added instead of subtracted |
| reversal / 红字 documents | revenue counted twice |
| `WAERS` / `MEINS` — currency and unit | CNY added to USD, pieces to tonnes |
| header ↔ line-item tables | a header field repeated per line: fan-out |
| deletion *flags* rather than deleted rows | cancelled documents counted as output |
| zero-padded codes (`000010`, material numbers) | landed as integers, they no longer join back |

The last one is handled at load: a digit run with a leading zero stays text.
The rest are modelling decisions, which is what the model file is for. On a
demo warehouse, summing `NETWR` without filtering `MANDT` or reading `SHKZG`
came out **207% high** — and the query looked perfect.

So the rule stands: every metric reconciles to a control query, and only a
figure the customer already publishes — their VF05 run, their group report —
makes the delivery report say `VERIFIED` rather than `SELF-CONSISTENT`.

## What the customer's data does not bring with it

SAP's authorization objects do not come along with the rows. Who may see which
plant's costs has to be declared again in `governance`. It is in scope, and it
is not free.

## Real-time values

"Where is this shipment now" cannot be answered from a copy. That goes through
a direct read-only tool against the API, marked in the UI as **outside the
governed path**: not a metric, not reconciled, not for reporting.
