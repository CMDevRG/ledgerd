# 0006. Reconciliation reads one consistent snapshot and only reports

Status: accepted

## Context

Balances are stored (a cache of the sum of postings) so that reads and overdraft
checks are cheap. A cache can drift if there is a bug or tampering. We want to be
able to *prove*, at any time and while the system is busy, that it has not.

## Decision

Reconciliation recomputes each account balance from its postings and compares it
with the stored one; checks that total debits equal total credits per currency;
and, optionally (`deep`), checks that every posting's `balance_after` follows from
the previous posting of its account.

It runs in a single `REPEATABLE READ`, `READ ONLY` transaction: all checks see the
same moment, concurrent commits are simply not part of it, it takes no row locks,
and the database refuses any write from it. It reports and never repairs.

## Consequences

- No false alarms from writes in flight, and writers are never blocked by it. Tests
  run reconciliations continuously while six writers commit transfers (every report
  must be clean), and run one while another session holds an uncommitted lock.
- It scans the whole ledger synchronously. For a large ledger it should become a
  background job, ideally per account range. Listed in the limitations.
- Detection is demonstrated by tampering as a superuser (changing a balance, a
  posting amount, or a `balance_after`) and checking each is reported.
- The benchmark ends with a deep reconciliation and fails if anything is off, so
  load is also a correctness test.

## Alternatives considered

- Repairing automatically: hides the problem and destroys the evidence.
- Running at `READ COMMITTED`: each statement would see a different moment, so
  separate checks could disagree with each other while writes happen.
