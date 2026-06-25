-- Meridian Retail — normalized model (matches the course).
-- Deliberately NOT a single pre-aggregated fact, so the join graph reproduces:
--   * fanout trap   : orders 1:many order_items
--   * chasm trap    : orders has two 1:many children (order_items AND refunds)
--   * missing edge  : order_items -> suppliers only via products
--   * PII masking   : customers.email
--   * row security  : stores.region

CREATE TABLE suppliers (
    supplier_id   INT PRIMARY KEY,
    supplier_name TEXT NOT NULL,
    country       TEXT NOT NULL
);

CREATE TABLE products (
    product_id   INT PRIMARY KEY,
    product_name TEXT NOT NULL,
    category     TEXT NOT NULL,
    subcategory  TEXT NOT NULL,
    brand        TEXT NOT NULL,
    supplier_id  INT NOT NULL REFERENCES suppliers(supplier_id),
    unit_cost    NUMERIC(10,2) NOT NULL
);

CREATE TABLE stores (
    store_id   INT PRIMARY KEY,
    store_name TEXT NOT NULL,
    region     TEXT NOT NULL,           -- row-level security key
    city       TEXT NOT NULL,
    store_type TEXT NOT NULL            -- flagship | standard | outlet
);

CREATE TABLE customers (
    customer_id   INT PRIMARY KEY,
    customer_name TEXT NOT NULL,
    segment       TEXT NOT NULL,        -- consumer | smb | enterprise
    loyalty_tier  TEXT NOT NULL,        -- bronze | silver | gold | platinum
    email         TEXT NOT NULL,        -- PII — masked dimension
    signup_date   DATE NOT NULL
);

-- Order header: date / store / customer / status live here (grain = one order).
CREATE TABLE orders (
    order_id    BIGINT PRIMARY KEY,
    order_date  DATE NOT NULL,
    store_id    INT  NOT NULL REFERENCES stores(store_id),
    customer_id INT  NOT NULL REFERENCES customers(customer_id),
    status      TEXT NOT NULL           -- completed | returned | pending
);

-- Line items: grain = one line. quantity*unit_price is additive at THIS grain only.
CREATE TABLE order_items (
    item_id    BIGINT PRIMARY KEY,
    order_id   BIGINT NOT NULL REFERENCES orders(order_id),
    product_id INT    NOT NULL REFERENCES products(product_id),
    quantity   INT    NOT NULL,
    unit_price NUMERIC(10,2) NOT NULL
);

-- Refunds: second 1:many child of orders (chasm trap with order_items).
CREATE TABLE refunds (
    refund_id     BIGINT PRIMARY KEY,
    order_id      BIGINT NOT NULL REFERENCES orders(order_id),
    refund_amount NUMERIC(12,2) NOT NULL,
    refund_date   DATE NOT NULL
);

CREATE INDEX idx_oi_order   ON order_items(order_id);
CREATE INDEX idx_oi_product ON order_items(product_id);
CREATE INDEX idx_ord_store  ON orders(store_id);
CREATE INDEX idx_ord_cust   ON orders(customer_id);
CREATE INDEX idx_ord_date   ON orders(order_date);
CREATE INDEX idx_ref_order  ON refunds(order_id);
