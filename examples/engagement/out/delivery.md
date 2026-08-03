# Delivery report — Conglomerate Retail · warehouse

**PARTIAL — 6/28 metrics reconcile, 22 have no control query, none is anchored to a customer figure**

| | |
|---|---|
| Semantic model | `/Users/liliang/Things/AI/base/dataintelligence/examples/engagement/models/warehouse.yaml` |
| Entities · joins | 7 · 8 |
| Dimensions · metrics | 13 · 28 |

## Not verified (22)

These metrics compile, and nothing checks that they are right. Each needs a
control query written by someone who knows the business:

- `category_count`
- `employee_count`
- `employee_monthly_salary_sum`
- `overhead_count`
- `overhead_rent_sum`
- `overhead_utilities_sum`
- `overhead_tax_sum`
- `overhead_labor_sum`
- `procurement_count`
- `procurement_purchase_qty_sum`
- `procurement_purchase_amount_sum`
- `procurement_purchase_amount_per_purchase_qty`
- `sale_count`
- `sale_transactions_sum`
- `sale_units_sum`
- `sale_cogs_amount_sum`
- `sale_revenue_per_unit`
- `shrinkage_count`
- `shrinkage_loss_qty_sum`
- `shrinkage_loss_amount_sum`
- `shrinkage_loss_amount_per_loss_qty`
- `store_area_sqm_sum`

## Metric reconciliation — 6/6

Every metric computed through the semantic layer, then again with a control
query written by hand. They must agree.

Where the expected figure came from matters as much as whether it
matched. A control query derived from the same schema by the same person
proves the model is self-consistent; only a figure the customer already
publishes proves it is right.

| Metric | Semantic layer | Control | Anchor | |
|---|---:|---:|---|---|
| `sale_revenue_sum` | 235184659.92 | 235184659.92 | *derived* | ✓ |
| `sale_gross_margin` | 0.2346 | 0.2346 | *derived* | ✓ |
| `sale_revenue_per_transaction` | 134.9217 | 134.9217 | *derived* | ✓ |
| `shrinkage_rate` | 0.0316 | 0.0316 | *derived* | ✓ |
| `sales_per_sqm` | 16191.715 | 16191.715 | *derived* | ✓ |
| `store_count` | 10 | 10 | *derived* | ✓ |

**6 of 6 controls have no external anchor.** Those check that the model
agrees with itself. To turn them into evidence, replace the expected value
with a figure the customer already publishes and record where it came from.

- **sale_gross_margin** — One grain, so the two sides are summed first and divided once.

- **sale_revenue_per_transaction** — An average of totals, not an average of per-row averages.

- **shrinkage_rate** — Each fact table aggregated to its own grain before the division.

- **sales_per_sqm** — The one worth checking. Joining sales to stores repeats each shop's area once per sales row (144 of them), inflating the denominator 144×. The query runs clean and the answer is wrong by two orders of magnitude.


## Natural-language accuracy — 0% (0/37)

Labelled questions asked end to end: grounded, governed, executed, and the
numbers compared with the control.

| Category | Accuracy | |
|---|---:|---|
| governance | 0% | 0/5 |
| grouped | 0% | 0/17 |
| ratio | 0% | 0/4 |
| simple | 0% | 0/8 |
| time | 0% | 0/3 |

10 case(s) skipped — they need an LLM and none was configured.

Latency p50 5ms · p95 6ms · max 12ms.

### Questions still answered wrong (37)

- **total_revenue_overall** — "what is total revenue" → got [sale_revenue_sum], expected [total_revenue]
- **total_revenue_sales_phrasing** — "show me total sales" → got [sales_per_sqm], expected [total_revenue]
- **total_revenue_topline_phrasing** — "what's our gross revenue" → got [sale_revenue_sum], expected [total_revenue]
- **units_sold_overall** — "how many units sold" → got [sale_units_sum], expected [units_sold]
- **units_sold_quantity_phrasing** — "total quantity sold" → got [category_count employee_count employee_monthly_salary_sum overhead_count overhead_rent_sum overhead_utilities_sum overhead_tax_sum overhead_labor_sum procurement_count procurement_purchase_qty_sum procurement_purchase_amount_sum procurement_purchase_amount_per_purchase_qty sale_count sale_revenue_sum sale_transactions_sum sale_units_sum sale_cogs_amount_sum sale_gross_margin sale_revenue_per_transaction sale_revenue_per_unit shrinkage_count shrinkage_loss_qty_sum shrinkage_loss_amount_sum shrinkage_loss_amount_per_loss_qty store_count store_area_sqm_sum shrinkage_rate sales_per_sqm], expected [units_sold]
- **order_count_overall** — "number of orders" → got [category_count], expected [order_count]
- **refund_total_overall** — "total refunds" → got [category_count employee_count employee_monthly_salary_sum overhead_count overhead_rent_sum overhead_utilities_sum overhead_tax_sum overhead_labor_sum procurement_count procurement_purchase_qty_sum procurement_purchase_amount_sum procurement_purchase_amount_per_purchase_qty sale_count sale_revenue_sum sale_transactions_sum sale_units_sum sale_cogs_amount_sum sale_gross_margin sale_revenue_per_transaction sale_revenue_per_unit shrinkage_count shrinkage_loss_qty_sum shrinkage_loss_amount_sum shrinkage_loss_amount_per_loss_qty store_count store_area_sqm_sum shrinkage_rate sales_per_sqm], expected [refund_total]
- **refund_total_returns_phrasing** — "how much in returns" → got [category_count employee_count employee_monthly_salary_sum overhead_count overhead_rent_sum overhead_utilities_sum overhead_tax_sum overhead_labor_sum procurement_count procurement_purchase_qty_sum procurement_purchase_amount_sum procurement_purchase_amount_per_purchase_qty sale_count sale_revenue_sum sale_transactions_sum sale_units_sum sale_cogs_amount_sum sale_gross_margin sale_revenue_per_transaction sale_revenue_per_unit shrinkage_count shrinkage_loss_qty_sum shrinkage_loss_amount_sum shrinkage_loss_amount_per_loss_qty store_count store_area_sqm_sum shrinkage_rate sales_per_sqm], expected [refund_total]
- **aov_overall** — "average order value" → got [sale_revenue_per_transaction], expected [avg_order_value]
- **aov_acronym_phrasing** — "what is our aov" → got [category_count employee_count employee_monthly_salary_sum overhead_count overhead_rent_sum overhead_utilities_sum overhead_tax_sum overhead_labor_sum procurement_count procurement_purchase_qty_sum procurement_purchase_amount_sum procurement_purchase_amount_per_purchase_qty sale_count sale_revenue_sum sale_transactions_sum sale_units_sum sale_cogs_amount_sum sale_gross_margin sale_revenue_per_transaction sale_revenue_per_unit shrinkage_count shrinkage_loss_qty_sum shrinkage_loss_amount_sum shrinkage_loss_amount_per_loss_qty store_count store_area_sqm_sum shrinkage_rate sales_per_sqm], expected [avg_order_value]
- **revenue_by_region** — "revenue by store region" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_store_type** — "revenue by store type" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_category** — "revenue by product category" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_brand** — "revenue by product brand" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_supplier_country** — "revenue by supplier country" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_segment** — "revenue by customer segment" → got [sale_revenue_sum], expected [total_revenue]
- **revenue_by_order_status** — "revenue by order status" → got [sale_revenue_sum], expected [total_revenue]
- **units_by_region** — "units sold by store region" → got [sale_units_sum], expected [units_sold]
- **units_by_category** — "units sold by product category" → got [sale_units_sum], expected [units_sold]
- **units_by_brand** — "units sold by product brand" → got [sale_units_sum], expected [units_sold]
- **units_by_store_type** — "units sold by store type" → got [sale_units_sum], expected [units_sold]
- **units_by_supplier_country** — "units sold by supplier country" → got [sale_units_sum], expected [units_sold]
- **units_by_segment** — "units sold by customer segment" → got [sale_units_sum], expected [units_sold]
- **orders_by_segment** — "order count by customer segment" → got [category_count], expected [order_count]
- **orders_by_region** — "order count by store region" → got [store_count], expected [order_count]
- **orders_by_store_type** — "order count by store type" → got [store_count], expected [order_count]
- **orders_by_status** — "order count by order status" → got [category_count], expected [order_count]
- **aov_by_region** — "average order value by store region" → got [store_count], expected [avg_order_value]
- **aov_by_segment** — "average order value by customer segment" → got [sale_revenue_per_transaction], expected [avg_order_value]
- **revenue_by_date** — "total revenue by order date" → got [sale_revenue_sum], expected [total_revenue]
- **units_by_date** — "units sold by order date" → got [sale_units_sum], expected [units_sold]
- **orders_by_date** — "order count by order date" → got [category_count], expected [order_count]
- **net_revenue_refused_for_analyst** — "what is net revenue" → got [sale_revenue_sum], expected [net_revenue]
- **net_revenue_refused_for_manager** — "what is net revenue" → got [sale_revenue_sum], expected [net_revenue]
- **net_revenue_allowed_for_finance** — "what is net revenue" → got [sale_revenue_sum], expected [net_revenue]
- **net_revenue_allowed_for_admin** — "what is net revenue" → got [sale_revenue_sum], expected [net_revenue]
- **pii_email_masked_for_analyst** — "revenue by customer email" → got [sale_revenue_sum], expected [total_revenue]

---

Reproduce: `di report -model /Users/liliang/Things/AI/base/dataintelligence/examples/engagement/models/warehouse.yaml -dsn <dsn>`
