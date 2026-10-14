# 0003. The idempotency key is claimed inside the ledger transaction

Status: accepted

## Context

Networks fail after the server has acted but before the client hears about it, so
clients retry. A retry of "pay 100" must not pay twice. The server needs to
recognise a repeated request, and must stay correct when the repeats arrive at the
same instant.

## Decision

`Idempotency-Key` is required on writes. Inside the **same database transaction**
that records the transaction, the service:

1. inserts the key with `ON CONFLICT DO NOTHING`;
2. if the insert wins, performs the operation and stores the outcome on the key row;
3. if the key already exists, compares the stored request hash and replays the
   stored result (same key, different request: `idempotency_key_reuse`).

The hash covers the HTTP method, the path and the *parsed, normalised* request
(fixed field order, UUIDs in lower case, amounts as canonical strings), so
whitespace and key order in the client's JSON do not matter.

## Why this works

- The key and the money movement commit or roll back **together**. A request that
  fails leaves no key behind, so retrying after fixing the cause (for example after
  funds arrive) works. There is no "in progress" state to get stuck in.
- If two identical requests arrive together, the second insert blocks on the
  first's uncommitted index entry until it commits or aborts; PostgreSQL does the
  waiting for us. Afterwards the second sees the committed key (and replays) or
  none (and proceeds). Tests release 50 identical requests: exactly one effect.
- Only successes are stored. A rejected request (validation, insufficient funds)
  does not consume its key.

## Consequences

- A duplicate waits for the first request to finish, bounded by `lock_timeout`.
- Keys are kept for at least 24 hours (48 by default) and purged by a background
  task; the retention floor is enforced in configuration and in the service.
- Replays return the stored *transaction*, not a stored response body. The
  transaction is immutable, so the replay is identical, and the service layer stays
  independent of the HTTP format.

## Alternatives considered

- A separate transaction to claim the key first: opens a window where the key exists
  but the effect does not, needing an "in progress" state, timeouts and cleanup.
- An in-memory or Redis cache of keys: loses atomicity with the ledger write.
