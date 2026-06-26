# Contributing

Thanks for considering a contribution. This project favors small, focused changes
with tests.

## Build & test

```bash
go build ./...
go test ./...
```

Two warehouse-backed gates run against a local Postgres (see `deploy/`):

```bash
di eval      # every metric reconciles to a hand-written control query (5/5)
di nleval    # NL accuracy gate over the labeled set (per-category floor)
di model lint -model models/meridian.yaml   # metadata gate
di threats   # threat-model-as-code gate
```

A change should keep all of these green.

## Conventions

- Standard Go layout; `_test.go` next to source. Keep packages focused.
- Match the surrounding code's style, comment density, and naming.
- New capabilities that are agent-callable belong in the MCP tool definitions.

## The one rule that matters: stay domain-neutral

The platform binary must contain **no customer or business logic**. Connectors
are generic adapters keyed by type + config; flows are config-driven primitives;
the semantic model, sources, and policies are data files. `examples/meridian/` is
*one example integration* — never hard-code a customer into the platform. If you
find yourself writing a customer's metric, table, or rule in Go, it belongs in a
config/example file instead.

Generic, reusable capabilities should be pushed down into the libraries
(`semantic-go` for the semantic layer/compiler, `cortexdb` for retrieval), not
kept in the product.

## Submitting

Open an issue describing the change first for anything non-trivial. Keep commits
scoped and messages descriptive.
