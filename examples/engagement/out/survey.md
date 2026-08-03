# Site survey — Conglomerate Retail · warehouse

pgx · 2026-08-03 12:17

Everything below came from a query against the live database. Nothing is
inferred from table or column names.

## Summary

- 7 tables, 4,964 rows in total
- 4 tables all stop at 2025-12-01: overheads, procurement, sales, shrinkage — one event, not 4 problems. A load that stopped, or a migration nobody mentioned?

## Things to ask about (5 table(s))

**employees** — 388 rows

- hire_date stops at 2023-04-11 — has this feed stopped?

**overheads** — 240 rows

- month stops at 2025-12-01 — has this feed stopped?

**procurement** — 1,440 rows

- month stops at 2025-12-01 — has this feed stopped?

**sales** — 1,440 rows

- month stops at 2025-12-01 — has this feed stopped?

**shrinkage** — 1,440 rows

- month stops at 2025-12-01 — has this feed stopped?

## Inventory

| Table | Rows | Key | Columns |
|---|---:|---|---:|
| `categories` | 6 | category_id | 2 |
| `employees` | 388 | emp_id | 6 |
| `overheads` | 240 | oh_id | 7 |
| `procurement` | 1,440 | proc_id | 6 |
| `sales` | 1,440 | sale_id | 8 |
| `shrinkage` | 1,440 | loss_id | 6 |
| `stores` | 10 | store_id | 7 |

## Columns

### `categories`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `category_id` | integer | — | 6 |  |
| `name` | text | — | 6 | `Beverages` `Fresh` `Frozen` `Grocery` `Health` `Household` |

### `employees`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `emp_id` | integer | — | many |  |
| `store_id` | integer | — | 10 |  |
| `position` | text | — | 4 | `cashier` `manager` `stocker` `supervisor` |
| `monthly_salary` | numeric | — | 17 |  |
| `hire_date` | date | — | many | 2019-01-14 → 2023-04-11 |
| `active` | boolean | — | 2 | `false` `true` |

### `overheads`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `oh_id` | bigint | — | many |  |
| `store_id` | integer | — | 10 |  |
| `month` | date | — | 24 | 2024-01-01 → 2025-12-01 |
| `rent` | numeric | — | 48 |  |
| `utilities` | numeric | — | 12 |  |
| `tax` | numeric | — | 2 |  |
| `labor` | numeric | — | 48 |  |

### `procurement`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `proc_id` | bigint | — | many |  |
| `store_id` | integer | — | 10 |  |
| `category_id` | integer | — | 6 |  |
| `month` | date | — | 24 | 2024-01-01 → 2025-12-01 |
| `purchase_qty` | integer | — | many |  |
| `purchase_amount` | numeric | — | many |  |

### `sales`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `sale_id` | bigint | — | many |  |
| `store_id` | integer | — | 10 |  |
| `category_id` | integer | — | 6 |  |
| `month` | date | — | 24 | 2024-01-01 → 2025-12-01 |
| `revenue` | numeric | — | many |  |
| `transactions` | integer | — | many |  |
| `units` | integer | — | many |  |
| `cogs_amount` | numeric | — | many |  |

### `shrinkage`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `loss_id` | bigint | — | many |  |
| `store_id` | integer | — | 10 |  |
| `category_id` | integer | — | 6 |  |
| `month` | date | — | 24 | 2024-01-01 → 2025-12-01 |
| `loss_qty` | integer | — | many |  |
| `loss_amount` | numeric | — | many |  |

### `stores`

| Column | Type | Null | Distinct | Range / values |
|---|---|---:|---:|---|
| `store_id` | integer | — | 10 |  |
| `name` | text | — | 10 | `Store 1` `Store 10` `Store 2` `Store 3` `Store 4` `Store 5` `Store 6` `Store 7` `Store 8` `Store 9` |
| `city` | text | — | 5 | `Beijing` `Chengdu` `Guangzhou` `Shanghai` `Shenzhen` |
| `region` | text | — | 4 | `East` `North` `South` `West` |
| `format` | text | — | 2 | `flagship` `neighbourhood` |
| `area_sqm` | numeric | — | 10 |  |
| `open_date` | date | — | 10 | 2018-09-04 → 2023-04-14 |
