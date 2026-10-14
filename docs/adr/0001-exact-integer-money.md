# 0001. Money is an exact integer (`NUMERIC(38,0)`), and a string on the wire

Status: accepted

## Context

A ledger that is off by one unit is wrong. Three places can silently change a
number: the database type, the programming language, and JSON.

- Floating point (`float64`, SQL `FLOAT`/`DOUBLE`) cannot represent most decimal
  fractions and loses integers above 2^53.
- `BIGINT` is exact but tops out near 9.2×10^18. One ether is 10^18 wei, so a
  `BIGINT` ledger overflows at about nine ether.
- Most JSON parsers (JavaScript's among them) read numbers as 64-bit floats, so an
  integer above 2^53 can be rounded *before it ever reaches the server*.

## Decision

- Amounts and balances are whole numbers of **minor units** (cents, wei, ...),
  stored as `NUMERIC(38,0)` and held in Go as `*big.Int`.
- On the wire they are **JSON strings** of decimal digits. A JSON number in a money
  field is rejected with `422`, even a small one, because we cannot know whether it
  was already rounded.
- A single posting is limited to 10^30 (31 digits). The database `CHECK`, the Go
  parser and the OpenAPI schema all state the same limit.
- Amounts have exactly one textual form (no sign, no leading zeros, no decimal
  point, ASCII digits only), which also makes request hashing for idempotency
  stable.
- The service does not know an asset's decimals: `1000000` USDC is just a number
  here, and formatting it is the client's job.

## Consequences

- Exact arithmetic everywhere, with room for 18-decimal assets: tests push values
  from 2^53 to 10^30 through the HTTP API, the database, history, historical
  balances and reconciliation.
- `NUMERIC` and `big.Int` are slower and larger than `int64`. The benchmark is
  dominated by commit latency, not arithmetic, so this has not been the bottleneck.
- A small conversion layer (`internal/pgnum`) maps `pgtype.Numeric` to `big.Int`,
  because the driver may return `1000` as `{1, exp 3}` or `{1000, exp 0}` depending
  on the wire format. It has a property test through the real database.
- Clients must send and parse strings. The OpenAPI schema says so.

## Alternatives considered

- `BIGINT` with fixed decimals: faster, but overflows for 18-decimal assets.
- A decimal library (`shopspring/decimal`): not needed; there are no fractions.
- Strings in the database: loses `SUM`, comparison and the `CHECK (balance >= 0)`
  that protects the invariant.
