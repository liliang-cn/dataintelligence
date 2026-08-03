-- A small retail warehouse, so the walkthrough in README.md can be run rather
-- than read. Deterministic: everything is derived from generate_series, so the
-- figures in the committed survey and delivery report are the figures you get.
--
--   createdb conglomerate
--   psql -d conglomerate -f warehouse.sql
--
-- The data is synthetic. Two things about it are not incidental:
--
--   * The fact tables all stop at 2025-12-01. That is what `di survey` and
--     `di drift` are meant to catch — a load that stopped leaves every row
--     valid and the totals merely not growing, and nothing errors.
--   * `stores` has ten rows and a last opening in 2022. That is a chain that
--     stopped expanding, not a broken feed, and the checks have to tell the
--     two apart or people learn to skim them.

DROP TABLE IF EXISTS shrinkage, procurement, overheads, sales, employees, stores, categories CASCADE;

CREATE TABLE categories (
    category_id int PRIMARY KEY,
    name        text NOT NULL
);

CREATE TABLE stores (
    store_id   int PRIMARY KEY,
    name       text NOT NULL,
    city       text,
    region     text,
    format     text,
    area_sqm   numeric(10,2),
    open_date  date
);

CREATE TABLE employees (
    emp_id         int PRIMARY KEY,
    store_id       int REFERENCES stores(store_id),
    position       text,
    monthly_salary numeric(10,2),
    hire_date      date,
    active         boolean
);

-- The four fact tables share a grain: one row per store, per month (and per
-- category, where the measure has one). That is what lets a ratio across two of
-- them be arithmetic instead of a join.
CREATE TABLE sales (
    sale_id      bigserial PRIMARY KEY,
    store_id     int REFERENCES stores(store_id),
    category_id  int REFERENCES categories(category_id),
    month        date NOT NULL,
    revenue      numeric(14,2),
    transactions int,
    units        int,
    cogs_amount  numeric(14,2)
);

CREATE TABLE procurement (
    proc_id         bigserial PRIMARY KEY,
    store_id        int REFERENCES stores(store_id),
    category_id     int REFERENCES categories(category_id),
    month           date NOT NULL,
    purchase_qty    int,
    purchase_amount numeric(14,2)
);

CREATE TABLE shrinkage (
    loss_id     bigserial PRIMARY KEY,
    store_id    int REFERENCES stores(store_id),
    category_id int REFERENCES categories(category_id),
    month       date NOT NULL,
    loss_qty    int,
    loss_amount numeric(14,2)
);

CREATE TABLE overheads (
    oh_id     bigserial PRIMARY KEY,
    store_id  int REFERENCES stores(store_id),
    month     date NOT NULL,
    rent      numeric(12,2),
    utilities numeric(12,2),
    tax       numeric(12,2),
    labor     numeric(12,2)
);

INSERT INTO categories VALUES
    (1,'Fresh'), (2,'Grocery'), (3,'Household'), (4,'Beverages'), (5,'Frozen'), (6,'Health');

INSERT INTO stores
SELECT i,
       'Store ' || i,
       (ARRAY['Shanghai','Beijing','Guangzhou','Chengdu','Shenzhen'])[1 + (i % 5)],
       (ARRAY['East','North','South','West','South'])[1 + (i % 5)],
       CASE WHEN i <= 3 THEN 'flagship' ELSE 'neighbourhood' END,
       CASE WHEN i <= 3 THEN 2400 + i * 130 ELSE 620 + i * 45 END,
       DATE '2018-03-01' + (i * 187) * INTERVAL '1 day'
FROM generate_series(1, 10) i;

-- Hiring stops in 2023: an old maximum on a dimension's date column is not a
-- stopped feed, and the survey has to say so by staying quiet about it.
INSERT INTO employees
SELECT i,
       1 + (i % 10),
       (ARRAY['cashier','stocker','supervisor','manager'])[1 + (i % 4)],
       4200 + (i % 17) * 260,
       DATE '2019-01-10' + (i * 4) * INTERVAL '1 day',
       (i % 23) <> 0
FROM generate_series(1, 388) i;

-- 24 months × 10 stores × 6 categories. Flagships turn over more per square
-- metre, which is what makes sales-per-sqm worth asking for — and what a naive
-- join gets wrong by repeating each store's area once per sales row.
INSERT INTO sales (store_id, category_id, month, revenue, transactions, units, cogs_amount)
SELECT s, c,
       DATE '2024-01-01' + (m * INTERVAL '1 month'),
       ROUND((CASE WHEN s <= 3 THEN 180000 ELSE 42000 END
              * (1 + 0.11 * c) * (1 + 0.02 * m) * (1 + 0.05 * ((s + c + m) % 7)))::numeric, 2),
       900 + ((s * 31 + c * 17 + m * 7) % 700),
       3400 + ((s * 53 + c * 29 + m * 11) % 2600),
       ROUND((CASE WHEN s <= 3 THEN 180000 ELSE 42000 END
              * (1 + 0.11 * c) * (1 + 0.02 * m) * (1 + 0.05 * ((s + c + m) % 7))
              * (0.72 + 0.03 * ((s + c) % 4)))::numeric, 2)
FROM generate_series(1, 10) s, generate_series(1, 6) c, generate_series(0, 23) m;

INSERT INTO procurement (store_id, category_id, month, purchase_qty, purchase_amount)
SELECT s, c,
       DATE '2024-01-01' + (m * INTERVAL '1 month'),
       3200 + ((s * 41 + c * 23 + m * 13) % 2400),
       ROUND((CASE WHEN s <= 3 THEN 132000 ELSE 31000 END
              * (1 + 0.10 * c) * (1 + 0.02 * m))::numeric, 2)
FROM generate_series(1, 10) s, generate_series(1, 6) c, generate_series(0, 23) m;

INSERT INTO shrinkage (store_id, category_id, month, loss_qty, loss_amount)
SELECT s, c,
       DATE '2024-01-01' + (m * INTERVAL '1 month'),
       120 + ((s * 19 + c * 7 + m * 3) % 380),
       ROUND((CASE WHEN s <= 3 THEN 7900 ELSE 1900 END
              * (1 + 0.09 * c) * (1 + 0.03 * ((s + m) % 5)))::numeric, 2)
FROM generate_series(1, 10) s, generate_series(1, 6) c, generate_series(0, 23) m;

INSERT INTO overheads (store_id, month, rent, utilities, tax, labor)
SELECT s,
       DATE '2024-01-01' + (m * INTERVAL '1 month'),
       ROUND((CASE WHEN s <= 3 THEN 96000 ELSE 23000 END * (1 + 0.01 * m))::numeric, 2),
       ROUND((CASE WHEN s <= 3 THEN 21000 ELSE 5400 END * (1 + 0.04 * ((s + m) % 6)))::numeric, 2),
       ROUND((CASE WHEN s <= 3 THEN 38000 ELSE 9100 END)::numeric, 2),
       ROUND((CASE WHEN s <= 3 THEN 143000 ELSE 41000 END * (1 + 0.02 * m))::numeric, 2)
FROM generate_series(1, 10) s, generate_series(0, 23) m;

ANALYZE;
