#!/usr/bin/env bash
# Restore the full Meridian enterprise data scenario across the diverse source
# stack, tied to the Postgres warehouse (so cross-source joins on email/product
# actually resolve). Prereqs: the warehouse (deploy/) and the source stack
# (deploy/sources/) are up.
#
#   deploy:        cd deploy        && docker compose up -d
#   sources:       cd deploy/sources && docker compose up -d
#   seed:          bash examples/meridian/seed/seed_sources.sh
#
# Idempotent: each store is dropped/flushed and re-seeded.
set -euo pipefail

PGC=${PGC:-meridian-warehouse}            # warehouse container
psqlpg() { docker exec -i "$PGC" psql -U meridian -d meridian "$@"; }

echo "==> MySQL (online orders) — emails reference real PG customers"
docker exec -i src-mysql mysql -uchannel -pchannelpw channel 2>/dev/null <<'SQL'
DROP TABLE IF EXISTS web_orders;
CREATE TABLE web_orders (
  order_ref varchar(16) PRIMARY KEY, customer_email varchar(255), channel varchar(16),
  amount decimal(10,2), status varchar(16), placed_at datetime);
SQL
psqlpg -tA -F$'\t' -c "
SELECT 'W-'||(10000+row_number() OVER (ORDER BY customer_id)), email,
  (ARRAY['web','ios','android','marketplace'])[(customer_id%4)+1],
  round((40 + (customer_id*7 % 460))::numeric,2),
  (ARRAY['delivered','delivered','shipped','placed','returned'])[(customer_id%5)+1],
  to_char(date '2025-05-01' + (customer_id%30), 'YYYY-MM-DD')||' 10:00:00'
FROM customers ORDER BY customer_id LIMIT 150;" \
| awk -F'\t' 'BEGIN{print "INSERT INTO web_orders VALUES"} {gsub(/'\''/,"",$0); printf "%s(\"%s\",\"%s\",\"%s\",%s,\"%s\",\"%s\")", (NR>1?",":""),$1,$2,$3,$4,$5,$6} END{print ";"}' \
| docker exec -i src-mysql mysql -uchannel -pchannelpw channel 2>/dev/null

echo "==> MongoDB (product reviews)"
docker exec src-mongo mongosh --quiet meridian --eval '
db.reviews.drop();
const titles=["Love it","Great quality","As described","A bit pricey","Exceeded expectations","Would buy again","Not for me","Solid value"];
const bodies=["Shipping was fast and the product holds up.","Exactly what I needed for daily use.","Good but the color was slightly off.","Works well, no complaints after a month.","Better than the brand I used before.","Decent for the price, would recommend.","Returned one, the second was perfect.","My favorite purchase this year."];
let docs=[];
for(let i=1;i<=180;i++){ const p=((i*7)%24)+1; const r=1+((i*3)%5);
 docs.push({product_id:p, rating:(r<3?(i%5==0?2:4):r), title:titles[i%8], body:bodies[i%8], author:"member"+(((i*13)%300)+1), created_at:new Date(2025,4,1+(i%30))}); }
db.reviews.insertMany(docs); print("reviews: "+db.reviews.countDocuments());' >/dev/null

echo "==> Redis (live inventory: stock:<product>:<store>)"
docker exec src-redis sh -c '
redis-cli FLUSHALL >/dev/null
for p in $(seq 1 24); do for s in $(seq 1 8); do
  redis-cli SET "stock:$p:$s" "$(( (p*17 + s*31) % 60 + 3 ))" >/dev/null
done; done'

echo "==> MinIO/S3 (supplier price-list CSV from real products)"
psqlpg -tA -F',' -c "
SELECT 'SKU-'||lpad(product_id::text,4,'0'), replace(product_name,',',' '), unit_cost, round(unit_cost*1.04,2)
FROM products ORDER BY product_id;" > /tmp/_catalog.csv
printf 'supplier_sku,product_name,unit_cost,new_cost\n%s' "$(cat /tmp/_catalog.csv)" > /tmp/catalog.csv
docker run --rm --network sources_default -v /tmp/catalog.csv:/catalog.csv --entrypoint sh minio/mc:latest -c '
mc alias set m http://src-minio:9000 minioadmin minioadmin >/dev/null
mc mb m/supplier-feeds >/dev/null 2>&1 || true
mc cp /catalog.csv m/supplier-feeds/catalogs/catalog.csv >/dev/null'

echo "==> Kafka/Redpanda (order events)"
go run examples/meridian/seed/kafka_produce.go

echo "==> done. Verify:  MINIO_ACCESS_KEY=minioadmin MINIO_SECRET_KEY=minioadmin di source read -limit 3 <name>"
echo "    sources: online_orders(mysql) reviews(mongo) live_inventory(redis) supplier_catalog(s3) order_events(kafka)"
