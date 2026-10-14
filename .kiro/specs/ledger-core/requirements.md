# Requirements Document — ledgerd (ledger-core)

## Introduction

`ledgerd` is a small, open-source, double-entry accounting ledger exposed over an HTTP/JSON API and backed by PostgreSQL. It is a focused core, not a full product: its purpose is to demonstrate, with verifiable code and tests, the properties that matter in financial systems — balanced transactions, ACID integrity, idempotent writes, overdraft prevention under concurrency, and reconcilable balances.

This document uses the EARS notation (Easy Approach to Requirements Syntax). Each requirement has a user story and numbered acceptance criteria. Every criterion must be verifiable by an automated test or a documented benchmark.

### Glossary

- **Account**: a named ledger account in a single currency.
- **Posting**: a single debit or credit line against one account. Postings are the source of truth.
- **Transaction**: an atomic group of two or more postings that balance.
- **Minor units**: the smallest unit of an asset as a whole number (e.g. cents for USD, wei for ETH). Amounts are arbitrary-precision integers of up to 38 digits and are never handled as floating point.
- **Asset code**: the short uppercase identifier of the currency or asset an account holds (e.g. `USD`, `EUR`, `ETH`, `USDT`). The field is named `currency` in the API.
- **Normal balance**: the side (DEBIT or CREDIT) on which an account's balance increases. Balance = sum of postings on the normal side − sum on the opposite side.
- **Reversal**: a new transaction that offsets a previous one. Existing records are never edited or deleted.
- **Idempotency key**: a client-chosen string that makes a write request safe to retry.

### Scope

In scope (MVP): accounts, balanced transactions, immutability and reversals, overdraft prevention, concurrency safety, idempotency, current and historical balances, posting history, reconciliation, API-key authentication, health endpoint, reproducible tests and benchmark, one-command local setup.

Out of scope: currency conversion (FX), user interface, user/role management, multi-tenancy, holds/reservations, and event publishing (Kafka outbox). These may be added in a later phase.

---

## Requirements

### Requirement 1: Account management

**User Story:** As an API client, I want to create and read accounts, so that I can record money movements against them.

#### Acceptance Criteria

1. WHEN a client submits a valid create-account request (code, currency, type, normal balance, allow-negative flag) THEN the system SHALL create the account with a zero balance and return it with a generated unique id and creation timestamp.
2. WHEN a client submits an account whose `code` already exists THEN the system SHALL reject the request with a conflict error and SHALL NOT create a second account.
3. WHEN the `currency` is not an asset code of 2 to 10 uppercase letters or digits (for example `USD`, `ETH`, `USDT`) THEN the system SHALL reject the request with a validation error.
4. WHEN the `type` is not one of ASSET, LIABILITY, EQUITY, INCOME, EXPENSE, or the normal balance is not DEBIT or CREDIT THEN the system SHALL reject the request with a validation error.
5. WHEN a client requests an account by id THEN the system SHALL return its attributes and current balance, or a not-found error if it does not exist.
6. The system SHALL NOT allow an account's currency, type, or normal balance to change after creation.
7. WHEN the account `code` is not 1 to 64 characters of letters, digits, `.`, `_`, `:` or `-`, starting with a letter or digit, THEN the system SHALL reject the request with a validation error (restricting the alphabet prevents look-alike Unicode codes).

### Requirement 2: Balanced transactions

**User Story:** As an API client, I want to record a transaction made of several postings, so that every movement of money has an equal and opposite counterpart.

#### Acceptance Criteria

1. WHEN a client submits a transaction THEN the system SHALL require at least two postings and at most 100 postings.
2. WHEN a transaction is submitted THEN the system SHALL require, for each currency present, that the sum of DEBIT amounts equals the sum of CREDIT amounts.
3. IF the transaction is not balanced THEN the system SHALL reject it with a validation error and SHALL NOT persist any posting or change any balance.
4. WHEN a posting amount is not a string of decimal digits representing a whole number between 1 and 10^30 (no sign, decimal point, exponent, or leading zeros), or is sent as a JSON number, THEN the system SHALL reject the transaction with a validation error.
5. WHEN a posting references an account that does not exist THEN the system SHALL reject the transaction with a not-found error and SHALL NOT persist anything.
6. WHEN a posting's currency differs from its account's currency THEN the system SHALL reject the transaction with a validation error.
7. WHEN a transaction is accepted THEN the system SHALL persist all its postings and update all affected balances atomically, so that either every effect is visible or none is.
8. WHEN a transaction is accepted THEN the system SHALL record, on each posting, the resulting account balance after that posting.
9. WHEN a transaction is accepted THEN the system SHALL assign it a unique id and a server-generated timestamp; clients SHALL NOT be able to set the timestamp.

### Requirement 3: Immutability and reversals

**User Story:** As an auditor, I want the ledger to be append-only, so that history can always be trusted and corrections are traceable.

#### Acceptance Criteria

1. The system SHALL NOT expose any operation that updates or deletes an existing transaction or posting.
2. WHEN a client requests a reversal of an existing transaction THEN the system SHALL create a new transaction whose postings have the opposite direction and the same amounts and accounts, and that references the original transaction.
3. IF a transaction has already been reversed THEN the system SHALL reject a second reversal request with a conflict error.
4. IF a transaction to reverse does not exist THEN the system SHALL return a not-found error.
5. IF a reversal would violate Requirement 4 (overdraft prevention) THEN the system SHALL reject it with the same error as a regular transaction.
6. The database SHALL enforce append-only behavior for postings and transactions independently of application code (for example, by revoking or blocking UPDATE and DELETE).

### Requirement 4: Overdraft prevention

**User Story:** As a product owner, I want accounts that must not go negative to be protected, so that users can never spend money they do not have.

#### Acceptance Criteria

1. WHERE an account has `allow_negative = false`, IF applying a transaction would make its balance negative THEN the system SHALL reject the whole transaction with an insufficient-funds error and SHALL NOT change any balance.
2. WHERE an account has `allow_negative = true`, the system SHALL accept transactions that make its balance negative.
3. The system SHALL enforce the non-negative rule at two layers: in application logic, and as a database constraint that rejects any write that would violate it.
4. WHEN a transaction touches several accounts and one of them would be overdrawn THEN the system SHALL reject the entire transaction, not only the offending posting.

### Requirement 5: Concurrency safety

**User Story:** As an operator, I want correct balances under heavy parallel load, so that no combination of simultaneous requests can corrupt the ledger.

#### Acceptance Criteria

1. WHEN many transactions run concurrently against the same accounts THEN the final balance of every account SHALL equal the sum of its postings (no lost updates).
2. WHEN concurrent transactions compete for funds in a non-negative account THEN the system SHALL accept only as many as the balance allows, and no account SHALL ever be observed with a negative balance.
3. The system SHALL acquire locks on the accounts involved in a transaction in a deterministic order, so that concurrent transactions cannot deadlock each other.
4. IF the database reports a serialization failure or deadlock THEN the system SHALL retry the transaction a bounded number of times before returning a retryable error to the client.
5. Under any number of concurrent transactions, the sum of all DEBIT amounts and the sum of all CREDIT amounts across the whole ledger SHALL remain equal, per currency.

### Requirement 6: Idempotent writes

**User Story:** As an API client on an unreliable network, I want to retry a request safely, so that a retry never duplicates a money movement.

#### Acceptance Criteria

1. WHEN a create-transaction or reverse-transaction request omits the `Idempotency-Key` header THEN the system SHALL reject it with a validation error.
2. WHEN a request arrives with an idempotency key not seen before THEN the system SHALL process it normally and store the key together with a hash of the request and the response.
3. WHEN a request arrives with a key already used and an identical request body THEN the system SHALL return the original response and SHALL NOT create a new transaction.
4. WHEN a request arrives with a key already used and a different request body THEN the system SHALL reject it with an unprocessable-request error.
5. WHEN several requests with the same key arrive simultaneously THEN exactly one SHALL be processed and the others SHALL receive the same result or a retryable "in progress" response; exactly one transaction SHALL exist afterwards.
6. IF processing fails with a retryable error THEN the system SHALL NOT leave the key permanently marked as completed.
7. Idempotency keys SHALL be retained for at least 24 hours.

### Requirement 7: Balances (current and historical)

**User Story:** As an API client, I want to know an account's balance now and at a past moment, so that I can display statements and audit history.

#### Acceptance Criteria

1. WHEN a client requests an account balance THEN the system SHALL return the current balance in minor units, the currency, and the timestamp of the last posting.
2. WHEN a client requests a balance with an `as_of` timestamp THEN the system SHALL return the balance computed from all postings recorded at or before that instant.
3. WHEN `as_of` is in the future THEN the system SHALL return the current balance.
4. WHEN `as_of` is malformed THEN the system SHALL reject the request with a validation error.
5. WHEN an account has no postings THEN the balance SHALL be zero.

### Requirement 8: Posting history

**User Story:** As an API client, I want to list an account's movements in pages, so that I can show a statement without loading everything.

#### Acceptance Criteria

1. WHEN a client requests an account's postings THEN the system SHALL return them in reverse chronological order, with the transaction id, direction, amount, and balance after each posting.
2. The system SHALL paginate using an opaque cursor and a `limit` parameter (default 50, maximum 200).
3. WHEN more results exist THEN the response SHALL include a cursor for the next page; WHEN no more results exist THEN the cursor SHALL be absent.
4. WHEN new postings are added between page requests THEN pagination SHALL NOT skip or repeat postings that existed when the first page was read.
5. WHEN `limit` is out of range or the cursor is invalid THEN the system SHALL reject the request with a validation error.

### Requirement 9: Reconciliation

**User Story:** As an operator, I want to verify that stored balances match the postings, so that I can detect and prove the absence of accounting drift.

#### Acceptance Criteria

1. WHEN a reconciliation run is triggered THEN the system SHALL recompute every account's balance from its postings and compare it with the stored balance.
2. WHEN a run completes THEN the system SHALL report the number of accounts checked, the number of mismatches, and, for each mismatch, the account id, expected balance, and stored balance.
3. WHEN a run completes THEN the system SHALL also verify that total debits equal total credits per currency and report the result.
4. The reconciliation SHALL NOT modify any balance; it only reports.
5. WHILE a reconciliation run is in progress, the system SHALL continue to accept transactions, and the run SHALL use a consistent snapshot so concurrent writes do not produce false mismatches.

### Requirement 10: API conventions and error handling

**User Story:** As an API client, I want consistent, documented responses, so that I can integrate and handle failures reliably.

#### Acceptance Criteria

1. The system SHALL expose the API under the `/v1` prefix and accept and return JSON.
2. WHEN a request fails THEN the system SHALL return an error in the RFC 9457 `application/problem+json` format with a stable machine-readable error code.
3. The system SHALL map errors to HTTP status codes consistently: 400 malformed request, 401 unauthorized, 404 not found, 409 conflict, 413 body too large, 422 semantic validation errors (including unbalanced transaction, insufficient funds with error code `insufficient_funds`, and idempotency-key reuse with a different body), 503 retryable (with a `Retry-After` header), 500 unexpected.
4. IF an unexpected error occurs THEN the system SHALL NOT leak internal details (SQL, stack traces) in the response.
5. Money amounts and balances SHALL be serialized as JSON strings of decimal integers in minor units (to avoid precision loss in clients that parse JSON numbers as floating point); identifiers as UUIDs; timestamps as RFC 3339 UTC.
6. The system SHALL publish an OpenAPI 3 specification that matches the implemented behavior.
7. The system SHALL enforce a maximum request body size.

### Requirement 11: Authentication

**User Story:** As an operator, I want write and read endpoints protected, so that the service is not exposed unauthenticated.

#### Acceptance Criteria

1. WHEN a request to any `/v1` endpoint lacks a valid API key in the `Authorization` header THEN the system SHALL reject it with an unauthorized error.
2. The API key SHALL be supplied through configuration (environment variable) and SHALL NOT be hard-coded or logged.
3. The system SHALL compare API keys in constant time.
4. The documentation SHALL state clearly that API-key authentication is a minimal mechanism for the demo and that a production deployment requires stronger authentication and authorization.
5. The health endpoint SHALL be reachable without authentication.

### Requirement 12: Health and observability

**User Story:** As an operator, I want to know whether the service is alive and ready, and to read structured logs, so that I can run and debug it.

#### Acceptance Criteria

1. WHEN a client requests `/healthz` THEN the system SHALL return 200 if the process is running.
2. WHEN a client requests `/readyz` THEN the system SHALL return 200 only if the database is reachable, and 503 otherwise.
3. The system SHALL write structured (JSON) logs including a request id, method, path, status, and duration.
4. The system SHALL NOT write secrets or full request bodies to logs.
5. WHEN the process receives SIGTERM THEN the system SHALL stop accepting new requests, finish in-flight requests within a bounded timeout, and close database connections.

### Requirement 13: Verification (automated tests)

**User Story:** As a reviewer, I want automated proof that the ledger is correct, so that I can trust the claims in the README.

#### Acceptance Criteria

1. The repository SHALL include unit tests for domain rules (balance check, validation, reversal construction).
2. The repository SHALL include integration tests that run against a real PostgreSQL instance started by the test suite (testcontainers).
3. The repository SHALL include property-based tests that generate random transactions, including amounts larger than 2^63 and up to the 10^30 limit, and assert the global invariants (debits equal credits; stored balance equals the sum of postings; no non-negative account is negative).
4. The repository SHALL include a concurrency test that runs at least 1,000 simultaneous transfers across several accounts and asserts no lost updates, no overdrafts, no deadlocks, and a clean reconciliation.
5. The repository SHALL include a concurrent-idempotency test that sends at least 50 simultaneous requests with the same key and asserts exactly one transaction was created.
6. The repository SHALL include a precision test that stores and reads back amounts and balances above 2^53 and above 2^63 through the API and the database, and asserts they are returned exactly.
7. The test suite SHALL run with the Go race detector enabled in continuous integration.
8. Continuous integration SHALL run lint, tests, and an OpenAPI/spec consistency check on every push and pull request.

### Requirement 14: Performance evidence

**User Story:** As a reader of the project, I want reproducible performance numbers, so that I can judge the design with real data and not claims.

#### Acceptance Criteria

1. The repository SHALL include a k6 benchmark that can be run with a single documented command against the local Docker Compose stack.
2. The benchmark SHALL report throughput (transactions per second) and latency percentiles (p50, p95, p99), and SHALL end by running reconciliation and reporting zero mismatches.
3. The README SHALL publish the results together with the hardware, software versions, and configuration used, and SHALL NOT present them as production guarantees.
4. No numeric performance target is mandated for the MVP; the goal is to measure and document, not to claim a figure in advance.

### Requirement 15: Developer experience

**User Story:** As a reviewer or contributor, I want to start the project with one command, so that I can evaluate it in minutes.

#### Acceptance Criteria

1. WHEN a user runs `make up` (which creates a `.env` file with random secrets on first use and then runs `docker compose up --build --wait`) THEN the system SHALL start PostgreSQL and the API, apply database migrations, and report healthy. Credentials are never defaulted in the repository, so `docker compose up` on its own requires `.env` to exist.
2. The default host ports SHALL be 8085 for the API and 15432 for PostgreSQL, configurable by environment variables, so that the stack does not collide with other local projects.
3. The repository SHALL provide a Makefile (or equivalent) with targets for build, test, race test, lint, run, and benchmark.
4. All container images and tool versions used SHALL be pinned.
5. The README SHALL be written in English and include: purpose, architecture diagram, quick start, API examples, design decisions and trade-offs, test strategy, benchmark results, limitations, and the Spec-Driven Development workflow used (links to these spec files).
6. The repository SHALL NOT contain code, schemas, data, or credentials taken from any employer's proprietary systems.
