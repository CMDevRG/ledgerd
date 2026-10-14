# 0004. Invariants are enforced by the database, not only by the code

Status: accepted

## Context

Application code has bugs, and applications are not the only thing that touches a
database (migrations, consoles, other services, a future second writer). The
properties that make a ledger trustworthy should hold even then.

## Decision

Every guarantee is checked in Go **and** enforced by PostgreSQL, and a test proves
the database part with plain SQL, without any application code:

| Guarantee | Database mechanism |
|---|---|
| History cannot be changed | `BEFORE UPDATE/DELETE/TRUNCATE` triggers on `transactions` and `postings` that always raise; the application role has no privilege to do it either |
| A transaction balances | a deferred constraint trigger checks, at commit, debits = credits per currency and at least two postings |
| A protected account is never negative | `CHECK (allow_negative OR balance >= 0)` |
| A posting's currency matches its account | composite foreign key `(account_id, currency)` |
| A transaction is reversed at most once | `UNIQUE (reverses_id)` |
| Amounts are in range | `CHECK (amount > 0 AND amount <= 10^30)`, `NUMERIC(38,0)` |
| The service cannot rewrite or reshape data | two roles: `ledgerd_owner` runs migrations; `ledgerd_app` has `SELECT, INSERT` on the ledger tables, only column-level `UPDATE (balance, last_posting_at)` on `accounts`, and no DDL |

## Consequences

- A bug in the service, or someone with the application credentials, cannot
  rewrite history, unbalance a transaction or overdraw a protected account. Even
  the table owner and a superuser are stopped by the append-only triggers (a
  superuser can disable a trigger deliberately; reconciliation will then show it).
- There are two implementations of each rule to keep consistent. Tests cover both
  layers. For the append-only triggers, the balance trigger, the non-negative
  check, the currency foreign key and the column privileges, a mutation experiment
  (removing the safeguard from the migration) confirmed that a test fails without
  it; that exercise found one gap (a posting added to an already-committed
  transaction), which now has its own test.
- Slight cost: the balance trigger runs once per posting row at commit (at most 100
  postings per transaction).
- `TRUNCATE transactions` alone is stopped earlier, by foreign keys; the trigger is
  what stops `TRUNCATE ... CASCADE`.

## Alternatives considered

- Application-only checks: simpler, but one bug away from corrupting money.
- A single database role: simpler to run, but then the service could drop tables.
