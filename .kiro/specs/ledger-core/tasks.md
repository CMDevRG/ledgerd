# Implementation Plan — ledgerd (ledger-core)

Conventions:

- Tasks are ordered so that each one builds on the previous and leaves the repository in a working state (build + tests green).
- Each task lists the requirements it satisfies (`R<n>.<m>`) and its verification.
- "Done" means: code written, tests written and passing with `go test -race`, lint clean.
- Domain code is built first, without a database, so the core rules are proven before any SQL exists.

---

## Phase 1 — Foundation

- [x] 1. Bootstrap the repository
  - Initialize the Go module (path decided with the owner before this task), `.gitignore`, `LICENSE` (owner chooses; MIT or Apache-2.0 suggested), `Makefile` with targets `build`, `test`, `test-race`, `lint`, `run`, `bench`, `.golangci.yml` (v2 format), and an `.editorconfig`.
  - Pin Go toolchain version in `go.mod`.
  - Verify: `make build`, `make lint`, `make test-race` run (even if with no tests yet).
  - _Requirements: R15.3, R15.4_

- [x] 2. Configuration package
  - `internal/config`: parse and validate environment variables (`LEDGERD_ADDR`, `LEDGERD_DATABASE_URL`, `LEDGERD_MIGRATE_DATABASE_URL`, `LEDGERD_API_KEY`, `LEDGERD_IDEMPOTENCY_TTL`, `LEDGERD_MAX_BODY_BYTES`, `LEDGERD_LOG_LEVEL`) with documented defaults; refuse to start without an API key; enforce the 24 h minimum TTL.
  - Never print the API key in errors or logs.
  - Verify: table-driven unit tests for valid/invalid inputs.
  - _Requirements: R6.7, R10.7, R11.2_

## Phase 2 — Domain (pure Go, no database)

- [x] 3. Money type and strict amount parsing
  - `internal/ledger`: `Amount` wrapper over `*big.Int` with `ParseAmount(string)` using the strict pattern (no sign, no leading zeros, no decimals or exponent, 1 ≤ value ≤ 10³⁰) and `String()`.
  - Verify: unit tests for valid, boundary (10³⁰ and 10³⁰+1), and invalid inputs; property test that `Parse(String(x)) == x`.
  - _Requirements: R2.4_

- [x] 4. Domain types and validation
  - Types: `Account`, `Direction`, `NormalBalance`, `AccountType`, `Posting`, `Transaction`; asset-code validation (`^[A-Z0-9]{2,10}$`); account-creation validation.
  - Transaction validation: 2–100 postings, per-currency debit = credit, description length, no mixed-currency mistakes.
  - Verify: table-driven unit tests covering every validation rule.
  - _Requirements: R1.3, R1.4, R2.1, R2.2, R2.4_

- [x] 5. Balance application rules
  - Pure function that, given locked account states and a validated transaction, returns new balances and each posting's `balance_after` (sequential application per account), and decides overdraft on the **final** net balance for accounts with `allow_negative = false`.
  - Verify: unit tests (multi-posting same account, credit-normal vs debit-normal, exact-zero boundary, overdraft rejects the whole transaction); property tests with `rapid`: Σdebits = Σcredits preserved and no non-negative account ends negative, using amounts above 2⁶³.
  - _Requirements: R2.8, R4.1, R4.2, R4.4, R5.5_

- [x] 6. Reversal construction
  - Pure function building mirror postings (opposite direction, same accounts/amounts) from an existing transaction.
  - Verify: property test — applying a transaction and then its reversal restores every balance.
  - _Requirements: R3.2_

## Phase 3 — Database

- [x] 7. Roles and migrations
  - `deploy/initdb/01-roles.sql` creating `ledgerd_owner` and `ledgerd_app` (least privilege).
  - Goose migrations (embedded): tables, constraints, indexes, composite FK, `non_negative` check, append-only triggers (row-level and TRUNCATE), deferred balance constraint trigger, grants for `ledgerd_app` (including `UPDATE (balance, last_posting_at)` on `accounts` only).
  - Verify: migrations apply cleanly on an empty database and roll back.
  - _Requirements: R2.6, R3.1, R3.3, R3.6, R4.3, R15.1_

- [x] 8. Integration-test harness
  - `test/integration`: helper that starts `postgres:16-alpine` with testcontainers, creates roles, runs migrations as owner, and returns pools for owner and app roles. One container per package, schema reset per test.
  - Verify: smoke test connects and sees the schema.
  - _Requirements: R13.2_

- [x] 9. Database safeguard tests
  - As `ledgerd_app`: `UPDATE`/`DELETE`/`TRUNCATE` on `postings` and `transactions` fail; direct insert of an unbalanced transaction fails at commit; negative balance on a non-negative account fails; posting with mismatched currency fails; second reversal fails on the unique constraint; `UPDATE accounts SET code=...` is denied.
  - Verify: tests pass; each safeguard is proven independent of application code.
  - _Requirements: R2.2, R2.6, R3.1, R3.3, R3.6, R4.3_

## Phase 4 — Store layer

- [x] 10. sqlc setup and numeric conversion
  - `sqlc.yaml` (pgx/v5), query files for accounts, transactions, postings, idempotency keys; `internal/store` helpers converting `pgtype.Numeric` ⇄ `*big.Int`.
  - Verify: round-trip property test through the real database with values up to 10³⁰ and balances above 2⁶³; `sqlc generate` has no diff in CI.
  - _Requirements: R2.4, R13.6_

- [x] 11. Transaction helper with retry and lock timeout
  - Helper running a function inside a `READ COMMITTED` transaction with `SET LOCAL lock_timeout`, retrying SQLSTATE `40001` and `40P01` up to 3 times with jittered backoff, mapping `55P03` and exhausted retries to a retryable error.
  - Verify: tests that inject serialization failures (via a fake) and a real lock-timeout scenario.
  - _Requirements: R5.4_

- [x] 12. Account store operations
  - Create account (unique code → conflict), get by id, lock accounts `ORDER BY id FOR UPDATE`.
  - Verify: integration tests for create/conflict/get/not-found and that lock order is by id.
  - _Requirements: R1.1, R1.2, R1.5, R1.6, R5.3_

## Phase 5 — Service

- [x] 13. Create transaction (without idempotency yet)
  - Service method: validate → begin → lock accounts in order → verify currencies → apply rules → insert transaction and postings (`recorded_at = GREATEST(clock_timestamp(), last_posting_at)`) → update balances → commit.
  - Verify: integration tests for success, unbalanced, unknown account, currency mismatch, overdraft (single and multi-account), atomic rollback of everything on failure, `balance_after` correctness.
  - _Requirements: R2.1–R2.9, R4.1, R4.2, R4.4_

- [x] 14. Reversal
  - Reverse method: load original, build mirror postings, insert with `reverses_id`, apply overdraft rules; duplicate reversal → `already_reversed`; unknown → not found.
  - Verify: integration tests including overdraft-on-reversal and balances restored.
  - _Requirements: R3.2–R3.5_

- [x] 15. Idempotency
  - Insert key inside the ledger transaction (`ON CONFLICT DO NOTHING`), canonical request hashing (`method + path + canonical body`), replay of stored response, `idempotency_key_reuse` on hash mismatch, cleanup goroutine for expired keys.
  - Verify: integration tests for first call, replay, mismatch, failed attempt leaves no key, and expired-key cleanup.
  - _Requirements: R6.1–R6.7_

- [x] 16. Balances and posting history
  - Current balance with last-posting timestamp; `as_of` lookup (future → current, none → zero); keyset pagination with opaque cursor, default 50 / max 200, validation of `limit` and cursor.
  - Verify: integration tests including pagination stability while inserting concurrently, and `as_of` at boundaries.
  - _Requirements: R7.1–R7.5, R8.1–R8.5_

- [x] 17. Reconciliation
  - `internal/reconcile`: run in `REPEATABLE READ READ ONLY`; per-account recomputation, global debit/credit equality per currency, optional deep `balance_after` chain check; report structure.
  - Verify: tests that a clean ledger reports zero mismatches, and that a deliberately corrupted balance (via the owner role in the test) is detected; run reconciliation while writes continue and assert no false mismatches.
  - _Requirements: R9.1–R9.5_

## Phase 6 — HTTP API

- [x] 18. HTTP foundation
  - Router on `net/http` `ServeMux`; middleware: panic recovery, request id, structured JSON logging (no bodies, no secrets), body-size limit, API-key authentication with constant-time comparison; RFC 9457 problem+json writer with the error-code table from the design; timeouts on the server.
  - Verify: tests for 401, 413, malformed JSON, panic → 500 without leaking details, log output contains no secrets.
  - _Requirements: R10.1–R10.4, R10.7, R11.1–R11.3, R11.5, R12.3, R12.4_

- [x] 19. Handlers
  - Accounts, balance, postings, transactions (create/get/reverse), reconciliations; money fields as strings; JSON numbers for money rejected; `Idempotency-Key` required on writes; `Idempotent-Replayed` header on replay; consistent error mapping.
  - Verify: handler tests with a fake service plus end-to-end tests against the real database.
  - _Requirements: R1–R3, R6, R7, R8, R9, R10.5_

- [x] 20. Health, readiness, shutdown, and entrypoint
  - `/healthz`, `/readyz` (pings DB), `cmd/ledgerd` with `serve` and `migrate` subcommands, graceful shutdown on SIGINT/SIGTERM with 15 s deadline.
  - Verify: readiness returns 503 when the database is down; shutdown test finishes an in-flight request.
  - _Requirements: R12.1, R12.2, R12.5, R15.1_

- [x] 21. OpenAPI specification and contract tests
  - Write `docs/openapi.yaml` (amount fields as strings with a pattern; problem+json schemas); validate real responses against it in tests with `kin-openapi`.
  - Verify: CI fails if a handler response deviates from the spec.
  - _Requirements: R10.6, R13.8_

## Phase 7 — Proof of correctness under load

- [x] 22. Concurrency tests
  - 1,000 simultaneous transfers over several accounts (mix of funded and underfunded) with `-race`: no lost updates, no overdraft, no deadlock/timeouts, reconciliation clean; 50 simultaneous identical-key requests ⇒ exactly one transaction; simultaneous double reversal ⇒ exactly one success and one `409`; a hot-account scenario to document the contention ceiling.
  - Verify: tests pass repeatedly (run 20× locally to check for flakiness).
  - _Requirements: R3.3, R4, R5, R6.5, R13.4, R13.5_

- [x] 23. Precision test
  - End-to-end test creating transactions with amounts above 2⁵³ and 2⁶³ and near 10³⁰; assert exact round trip in API responses, balances, `as_of`, history, and reconciliation.
  - _Requirements: R13.6_

## Phase 8 — Packaging and evidence

- [x] 24. Docker image and Compose stack
  - Multi-stage `Dockerfile` (pinned base images, non-root user, static binary); `docker-compose.yml` with `postgres` (init script), `migrate` (one-shot), and `api` (waits for `migrate`); ports 8085 and 15432 overridable; health checks.
  - Verify: `docker compose up` from a clean clone reaches healthy; `curl` smoke test of create account → transaction → balance.
  - _Requirements: R15.1, R15.2, R15.4_

- [x] 25. k6 benchmark
  - `bench/k6/` script (setup creates accounts; scenarios: transfers between random pairs and a hot-account case); `make bench` runs it via the `grafana/k6` image; final step triggers reconciliation and fails the run on any mismatch.
  - Verify: results captured with hardware/software versions.
  - _Requirements: R14.1–R14.4_

- [x] 26. Continuous integration
  - GitHub Actions: lint, `go test -race ./...` (with Docker for testcontainers), `sqlc generate` diff check, OpenAPI validation, `govulncheck`, build of the Docker image.
  - _Requirements: R13.7, R13.8_

## Phase 9 — Documentation and release

- [x] 27. README and ADRs (in English)
  - README: purpose, architecture diagram, quick start, API examples, design decisions and trade-offs, test strategy, benchmark results with hardware, limitations (design §10), the Spec-Driven Development workflow with links to the three spec files.
  - ADRs in `docs/adr/`: isolation level and locking, idempotency inside the transaction, `NUMERIC(38,0)` and string amounts, append-only enforcement.
  - _Requirements: R15.5, R14.3_

- [ ] 28. Final review and release (review done; waiting for three decisions from the owner, see Status)
  - Checklist: no secrets or proprietary content (R15.6), no leftover TODOs, all requirements traced to a test, license present, git author email chosen deliberately, tag `v0.1.0`.
  - Publish to GitHub after the owner runs `gh auth login`.
  - Status: the review is complete. Done and verified: the whole tree and git history contain no employer names, credentials or `.env`; no TODOs; lint, generated code, traceability, `go mod verify`, `govulncheck` (binary) and `actionlint` are clean; the full suite passes with the race detector; a fresh clone runs `make up`, `make smoke` and a short `make bench` successfully. **Pending the owner:** (1) the GitHub user name, because the module path is currently `github.com/radadev/ledgerd`; (2) the e-mail that signs the commits (currently the one in the local git configuration); (3) `gh auth login`, then publishing and tagging `v0.1.0` on the final commit.
  - _Requirements: R15.6_

## Optional follow-ups (outside the MVP)

- [ ] 29. Holds / reservations (authorize, capture, release).
- [ ] 30. Transactional outbox publishing to Kafka, feeding a ClickHouse/Superset reporting pipeline (second portfolio project).
- [ ] 31. Prometheus metrics and a Grafana dashboard.
- [ ] 32. Article: "Preventing accounting drift with concurrent writes in PostgreSQL".
