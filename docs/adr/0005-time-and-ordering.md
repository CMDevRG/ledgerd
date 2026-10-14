# 0005. One timestamp per transaction, never earlier than the account's last posting

Status: accepted

## Context

Two features read history by time or by order: the balance *as of* an instant,
and paging through an account's postings. Both give wrong answers if time can go
backwards for an account, or if one transaction can straddle an instant.

## Decision

- The postings of a transaction all share **one** timestamp, equal to the
  transaction's `created_at`. A query at an instant therefore can never fall
  between two postings of the same transaction and show an intermediate balance.
- That timestamp is the application clock, but **not earlier than the
  `last_posting_at` of any account involved**. It is computed while the account
  locks are held, so an account's timestamps never decrease, even if the clock
  steps back or application servers disagree slightly.
- Timestamps are truncated to microseconds (PostgreSQL's resolution), so what Go
  compares equals what is stored, and a query's `as_of` is truncated the same way.
- Within one account, `postings.id` order is commit order (all writers hold the
  account lock when inserting). Paging uses **keyset pagination** on this id:
  `WHERE id < $cursor ORDER BY id DESC`. Postings added while someone pages only
  get higher ids, so no page shifts: nothing is skipped or repeated.
- The balance as of `T` is the `balance_after` of the latest posting with
  `recorded_at <= T`, ordered by `(recorded_at, id)`. If an account has several
  postings in one transaction, the highest id (the final balance) wins.

## Consequences

- Historical balances and pagination are exact, and tested with a clock that goes
  backwards, with transactions that have several postings on one account, and with
  writers adding postings while a client pages.
- A balance "as of" an instant slightly ahead of the application clock (possible
  after the guard moved a timestamp forward) is answered consistently from stored
  data.
- Intermediate `balance_after` values inside a multi-posting transaction can be
  transient (even negative on a protected account); only the last per account is a
  committed state. The API documentation says so.

## Alternatives considered

- Database `clock_timestamp()`/`now()`: `now()` is the transaction *start* time,
  which can precede earlier commits; `clock_timestamp()` ignores clock steps.
- `OFFSET` pagination: shifts when rows are added and gets slower with depth.
