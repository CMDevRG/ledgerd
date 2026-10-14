# 0002. `READ COMMITTED` with explicit, ordered row locks

Status: accepted

## Context

Two transactions that debit the same account at the same moment must not both see
the old balance (a lost update), and a protected account must never go negative.
PostgreSQL offers several ways to get there.

## Decision

Each transaction runs at `READ COMMITTED` and takes `SELECT ... FOR UPDATE` row
locks on every account it touches, **in ascending id order**, before reading
balances or writing anything. The new balances are computed in memory and written
before commit.

- The fixed order means two transactions can never wait on each other in a cycle,
  so deadlocks cannot form between ledger transactions. A deadlock or
  serialization failure that still happens (from some other source) is retried up
  to three times with jittered backoff, as a safety net.
- A statement waits for a lock for at most `lock_timeout` (3 s). If it expires the
  client gets `503` with `Retry-After`; the store does not retry it, because a lock
  held for seconds means a long queue, and retrying only lengthens it.
- Everything after the locks is one database transaction, so a rejected or failed
  transaction leaves nothing behind.

## Consequences

- Correct under concurrency with simple reasoning: after the locks, the balances
  read are the balances that will be written to. Tests release 1,000 simultaneous
  transfers and check no lost updates, no overdraft and a clean reconciliation.
- **A hot account is a throughput ceiling.** Writers that touch the same account
  queue behind each other for the whole transaction. The benchmark measures it
  (about a third of the spread-out throughput on the reference machine). The usual
  remedy is to split a hot account into sub-accounts and sum them.
- No spurious serialization failures, so the happy path has no retries.

## Alternatives considered

- `SERIALIZABLE`: needs no explicit locks, but every conflict becomes an error the
  application must retry, and under contention many transactions fail repeatedly.
- Optimistic concurrency (a `version` column and `UPDATE ... WHERE version = $n`):
  fine for rare conflicts, wasteful when many writers hit the same account.
- Advisory locks: work, but hide the dependency from the schema and the reader.

## Note on `ORDER BY`

The locking query has `ORDER BY id`. PostgreSQL would often lock in the same
order anyway (index or heap order), but that is a property of the plan, not a
promise. The `ORDER BY` turns it into a guarantee.
