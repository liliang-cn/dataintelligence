-- Deterministic seed so eval "known-good numbers" are stable.
SELECT setseed(0.42);

INSERT INTO suppliers
SELECT g, 'Supplier ' || g,
    (ARRAY['USA','China','Germany','Vietnam','Mexico','Japan'])[g]
FROM generate_series(1, 6) g;

INSERT INTO products
SELECT g,
    'Product ' || g,
    (ARRAY['Electronics','Apparel','Home','Grocery','Beauty','Sports'])[1 + ((g-1) % 6)],
    'Sub ' || (1 + ((g-1) % 3)),
    (ARRAY['Acme','Globex','Initech','Umbrella'])[1 + ((g-1) % 4)],
    1 + ((g-1) % 6),
    round((10 + random() * 190)::numeric, 2)
FROM generate_series(1, 24) g;

INSERT INTO stores
SELECT g, 'Store ' || g,
    (ARRAY['North','South','East','West'])[1 + ((g-1) % 4)],
    (ARRAY['NYC','LA','Chicago','Houston','Boston','Seattle','Miami','Denver'])[g],
    (ARRAY['flagship','standard','outlet'])[1 + ((g-1) % 3)]
FROM generate_series(1, 8) g;

INSERT INTO customers
SELECT g, 'Customer ' || g,
    (ARRAY['consumer','smb','enterprise'])[1 + ((g-1) % 3)],
    (ARRAY['bronze','silver','gold','platinum'])[1 + ((g-1) % 4)],
    'customer' || g || '@example.com',
    '2023-01-01'::date + ((g * 7) % 600)
FROM generate_series(1, 300) g;

-- Orders (header grain): 5000 orders
INSERT INTO orders
SELECT g,
    '2024-01-01'::date + (floor(random() * 731))::int,
    (1 + floor(random() * 8))::int,
    (1 + floor(random() * 300))::int,
    (ARRAY['completed','completed','completed','returned','pending'])[1 + floor(random() * 5)::int]
FROM generate_series(1, 5000) g;

-- Line items (line grain): 12000 items spread across orders.
-- Pick order_id/product_id/quantity per row first (volatile random per row),
-- then plain-join products for unit_cost. (A LATERAL with random() in its WHERE
-- gets evaluated once and collapses every row to one product.)
WITH gen AS (
    SELECT s,
        (1 + floor(random() * 5000))::int AS order_id,
        (1 + floor(random() * 24))::int  AS product_id,
        (1 + floor(random() * 5))::int   AS quantity
    FROM generate_series(1, 12000) s
)
INSERT INTO order_items
SELECT g.s, g.order_id, g.product_id, g.quantity,
    round((p.unit_cost * (1.3 + random() * 0.7))::numeric, 2) AS unit_price
FROM gen g
JOIN products p ON p.product_id = g.product_id;

-- Refunds: ~800 refunds on a subset of orders (second 1:many child → chasm)
INSERT INTO refunds
SELECT g,
    (1 + floor(random() * 5000))::int,
    round((5 + random() * 120)::numeric, 2),
    '2024-01-01'::date + (floor(random() * 731))::int
FROM generate_series(1, 800) g;
