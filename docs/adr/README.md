# Architecture decision records

Short notes on the decisions that shaped ledgerd, why they were made, and what
they cost. Each one is written so that a reader can disagree with it knowingly.

| # | Decision |
|---|---|
| [0001](0001-exact-integer-money.md) | Money is an exact integer (`NUMERIC(38,0)`), and a string on the wire |
| [0002](0002-isolation-and-locking.md) | `READ COMMITTED` with explicit, ordered row locks |
| [0003](0003-idempotency-inside-the-transaction.md) | The idempotency key is claimed inside the ledger transaction |
| [0004](0004-database-enforced-invariants.md) | Invariants are enforced by the database, not only by the code |
| [0005](0005-time-and-ordering.md) | One timestamp per transaction, never earlier than the account's last posting |
| [0006](0006-reconciliation-on-a-snapshot.md) | Reconciliation reads one consistent snapshot and only reports |
