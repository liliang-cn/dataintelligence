# Example domain: Reformd (boutique reformer pilates)

A second example integration, in a completely different business from Meridian
Retail — proof that the platform is domain-neutral. Studios, classes, members,
bookings, check-ins, and payments; a Solidcore-style 50-minute class menu.

## Run it

```bash
# 1. create a warehouse DB and load the schema + realistic seed
createdb fitness   # or: psql -c 'CREATE DATABASE fitness'
psql -d fitness -f examples/fitness/schema/01_schema.sql
psql -d fitness -f examples/fitness/schema/02_seed.sql

export FIT="postgres://USER:PASS@HOST:5432/fitness?sslmode=disable"

# 2. (optional) auto-generate a model draft from the schema — onboarding on a new domain
di model gen -dsn "$FIT" -out /tmp/fitness.yaml

# 3. query the curated model
di query -model examples/fitness/model.yaml -dsn "$FIT" -metrics fill_rate -by studio_city
di query -model examples/fitness/model.yaml -dsn "$FIT" -metrics no_show_rate,attendance_rate -by class_name
di query -model examples/fitness/model.yaml -dsn "$FIT" -metrics revenue -by member_tier -role finance

# 4. serve it (REST /v1 + MCP + /ui)
DI_DSN="$FIT" di serve -model examples/fitness/model.yaml
```

## What it demonstrates

- **Neutrality** — the same engine that runs Meridian Retail runs a fitness chain
  with zero code changes; only data + `model.yaml` differ.
- **Chasm-safe ratios across grains** — `fill_rate` divides confirmed bookings
  (booking grain) by seats offered (session grain); `no_show_rate` and
  `attendance_rate` divide within the booking grain. Each base aggregates in its
  own CTE, so nothing fans out.
- **Governance** — `revenue` is finance/admin only (RBAC); `member_email` is
  masked; `studio_region` is the row-level-security key.
- **Realistic data** — ~77% studio fill, ~12% no-show, real member names/emails,
  membership + class-pack + drop-in payments.
