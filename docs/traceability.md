# Traceability: requirement → tests

Each acceptance criterion of [requirements.md](../.kiro/specs/ledger-core/requirements.md)
and the automated tests that verify it. Test names are Go test functions (`-run`
accepts them); `test/integration` tests need Docker. `make check-docs` (also run in CI)
fails if a name below does not exist.

Criteria that are not (or not only) verified by an automated test say so.

## R1. Account management

| Criterion | Verified by |
|---|---|
| 1.1 create with zero balance, id, timestamp | `TestCreateAndGetAccount`, `TestCreateAccount`, `TestAPI_FullLifecycle` |
| 1.2 duplicate code → conflict, no second account | `TestCreateAccount_DuplicateCode`, `TestCreateAccount_Errors`, `TestAPI_ErrorResponses` |
| 1.3 currency format | `TestParseCurrency`, `TestNewAccount_Validate_Invalid`, `TestAmountAndFormatConstraints` (database `CHECK`) |
| 1.4 account type / normal balance | `TestAccountType_Valid`, `TestSide`, `TestNewAccount_Validate_Invalid` |
| 1.5 get by id / not found | `TestCreateAndGetAccount`, `TestGetAccount_NotFound`, `TestGetAccount` |
| 1.6 immutable currency, type, normal balance | `TestApplicationRoleHasLeastPrivilege` (column-level `UPDATE` privileges); the API has no update operation (`TestRoutesMatchTheSpecification`) |
| 1.7 account code alphabet | `TestNewAccount_Validate_Valid`, `TestNewAccount_Validate_Invalid`, `TestAmountAndFormatConstraints` |

## R2. Balanced transactions

| Criterion | Verified by |
|---|---|
| 2.1 2–100 postings | `TestValidate_PostingCount`, `TestBalancedTransactions` (database) |
| 2.2 debits = credits per currency | `TestValidate_Unbalanced`, `TestProperty_BalancedRequestsAreValid`, `TestProperty_FlippingOnePostingUnbalances`, `TestBalancedTransactions` |
| 2.3 unbalanced → rejected, nothing persisted | `TestCreateTransaction_Rejections_PersistNothing`, `TestAPI_FullLifecycle` |
| 2.4 amount format and range, JSON numbers refused | `TestParseAmount_Valid`, `TestParseAmount_Invalid`, `TestAmount_JSON`, `TestProperty_AcceptedInputIsCanonical`, `TestCreateTransaction_AmountMustBeAString`, `TestPrecision_TheLimitIsExact`, `TestAmountAndFormatConstraints` |
| 2.5 unknown account → not found | `TestApply_Errors`, `TestCreateTransaction_Rejections_PersistNothing` |
| 2.6 currency must match the account | `TestApply_Errors`, `TestCreateTransaction_Rejections_PersistNothing`, `TestCurrencyMustMatchAccount` (database) |
| 2.7 atomic: all or nothing | `TestCreateTransaction_DatabaseFailureMidwayRollsBackEverything`, `TestInTx_CommitsOnSuccessAndRollsBackOnError` |
| 2.8 `balance_after` on each posting | `TestCreateTransaction_BalanceAfterChain`, `TestCreateTransaction_SeveralPostingsOnTheSameAccount`, `TestApply_SeveralPostingsOnOneAccount`, `TestReconcile_DeepCheckFindsABrokenBalanceChain` |
| 2.9 server-side id and timestamp | `TestCreateTransaction_TimestampsAreServerSideAndMicrosecondPrecise` (the request has no timestamp field and unknown fields are rejected: `TestDecodeJSON`) |

## R3. Immutability and reversals

| Criterion | Verified by |
|---|---|
| 3.1 no update or delete operation | `TestRoutesMatchTheSpecification`, `TestUnmatchedRoutes` (405 for other methods) |
| 3.2 reversal mirrors the original | `TestReverse_MirrorsPostings`, `TestReverse_OffsetsTheOriginal`, `TestProperty_ReversalRestoresBalances`, `TestProperty_ReverseIsAnInvolution`, `TestReverse_MultiPostingMultiCurrency` |
| 3.3 second reversal → conflict | `TestReverse_CanOnlyHappenOnce`, `TestReverse_ConcurrentAttemptsExactlyOneWins`, `TestConcurrency_DoubleReversalOverHTTP`, `TestSingleReversal` (database) |
| 3.4 unknown transaction → not found | `TestReverse_UnknownTransaction` |
| 3.5 reversal obeys the overdraft rule | `TestReverse_ObeysTheOverdraftRule`, `TestReverse_UndoingASpentDepositIsRejected` |
| 3.6 append-only enforced by the database | `TestAppendOnly` (as app role, owner and superuser) |

## R4. Overdraft prevention

| Criterion | Verified by |
|---|---|
| 4.1 reject, change nothing | `TestApply_Overdraft`, `TestCreateTransaction_Rejections_PersistNothing`, `TestProperty_ApplyPreservesInvariants` |
| 4.2 `allow_negative` accounts may go below zero | `TestApply_Overdraft`, `TestCreateTransaction_AllowNegativeAccountsMayGoBelowZero` |
| 4.3 two layers (code and database) | `TestNonNegativeBalance` (database), `TestApply_Overdraft` (code) |
| 4.4 one overdrawn account rejects the whole transaction | `TestApply_Overdraft`, `TestCreateTransaction_Rejections_PersistNothing` |

## R5. Concurrency safety

| Criterion | Verified by |
|---|---|
| 5.1 no lost updates | `TestConcurrency_ThousandSimultaneousTransfers` |
| 5.2 exactly as many succeed as the balance allows; never negative | `TestConcurrency_ExactlyAsManyAsTheBalanceAllows`, `TestConcurrency_ThousandSimultaneousTransfers` |
| 5.3 deterministic lock order, no deadlocks | `TestLockAccounts`, `TestLockAccounts_NeverDeadlocksWhenIdsArriveInAnyOrder`, `TestInTx_ReportsDeadlockWhenRetriesAreExhausted` (shows what unordered locking does) |
| 5.4 bounded retry of deadlock / serialization failure | `TestRetrying_RetriesRetryableErrorsThenSucceeds`, `TestRetrying_GivesUpAfterMaxAttempts`, `TestInTx_RetriesARealDeadlock`, `TestInTx_LockTimeout` |
| 5.5 debits = credits across the ledger, per currency | `TestProperty_ApplyPreservesInvariants`, `TestReconcile_WhileTransactionsAreCommitting`, `TestConcurrency_ThousandSimultaneousTransfers` |

## R6. Idempotent writes

| Criterion | Verified by |
|---|---|
| 6.1 missing key → validation error | `TestIdempotency_KeyAndHashAreValidated`, `TestCreateTransaction_IdempotencyHeader` |
| 6.2 first use stores key, hash and result | `TestIdempotency_FirstCallStoresTheKeyWithItsResult` |
| 6.3 same key, same request → original result | `TestIdempotency_ReplayReturnsTheOriginalWithoutANewEffect`, `TestIdempotency_Reversals`, `TestCreateTransaction_ReplayIsFlagged`, `TestAPI_FullLifecycle` |
| 6.4 same key, different request → rejected | `TestIdempotency_SameKeyDifferentRequestIsRejected`, `TestCreateTransaction_RequestHashIsCanonical`, `TestIdempotency_RequestHashCoversMethodPathAndBody` |
| 6.5 simultaneous duplicates: exactly one effect | `TestIdempotency_ConcurrentDuplicatesHaveExactlyOneEffect`, `TestConcurrency_IdempotentDuplicatesOverHTTP`, `TestConcurrency_SameKeyWithConflictingBodies` |
| 6.6 a failed attempt does not consume the key | `TestIdempotency_AFailedAttemptDoesNotConsumeTheKey`, `TestIdempotency_ValidationErrorsAreNotStored` |
| 6.7 retention of at least 24 h | `TestIdempotency_Cleanup`, `TestIdempotency_CleanupLoopRunsUntilCancelled`, `TestIdempotency_RetentionMinimumMatchesConfiguration`, `TestLoadServe_Invalid` |

## R7. Balances

| Criterion | Verified by |
|---|---|
| 7.1 current balance, currency, last posting time | `TestBalance_Current`, `TestAPI_FullLifecycle` |
| 7.2 balance as of an instant (inclusive) | `TestBalance_AsOf`, `TestBalance_AsOfSeesTheFinalBalanceOfATransaction`, `TestBalance_AsOfStaysCorrectWhenTheClockGoesBackwards`, `TestAPI_BalanceAsOfAndPostingPagination` |
| 7.3 future instant → current balance | `TestBalance_AsOf`, `TestAPI_BalanceAsOfAndPostingPagination` |
| 7.4 malformed `as_of` → validation error | `TestGetBalance` |
| 7.5 no postings → zero | `TestBalance_Current`, `TestBalance_AsOf` |

## R8. Posting history

| Criterion | Verified by |
|---|---|
| 8.1 newest first, with the required fields | `TestListPostings_FieldsAndOrder` |
| 8.2 opaque cursor, default 50, maximum 200 | `TestListPostings_DefaultAndMaximumPageSize`, `TestCursor_RoundTrip` |
| 8.3 cursor present only when more exist | `TestListPostings_ExactlyOnePageHasNoCursor`, `TestListPostings_PaginatesWithoutGapsOrRepeats` |
| 8.4 stable while postings are added | `TestListPostings_StableWhileNewPostingsArrive`, `TestConcurrency_AccountHistoryIsASingleChain` |
| 8.5 invalid limit or cursor → validation error | `TestListPostings_Validation`, `TestCursor_RejectsGarbage`, `TestListPostings` |

## R9. Reconciliation

| Criterion | Verified by |
|---|---|
| 9.1 recompute and compare | `TestReconcile_CleanLedger`, `TestReconcile_DetectsACorruptedBalance` |
| 9.2 report: accounts checked, mismatches with id, expected, stored | `TestReconcile_DetectsACorruptedBalance`, `TestReconcile_ReportsEveryMismatchInIdOrder` |
| 9.3 debits = credits per currency | `TestReconcile_CleanLedger`, `TestReconcile_DetectsTamperedPostings` |
| 9.4 never modifies | `TestReconcile_DetectsACorruptedBalance`, `TestReconcile_UsesAConsistentReadOnlySnapshot` |
| 9.5 concurrent writes, consistent snapshot, no false alarms | `TestReconcile_WhileTransactionsAreCommitting`, `TestReconcile_UsesAConsistentReadOnlySnapshot`, `TestReconcile_DoesNotWaitForWriters` |

## R10. API conventions and error handling

| Criterion | Verified by |
|---|---|
| 10.1 `/v1`, JSON | `TestRoutesMatchTheSpecification`, `TestDecodeJSON` |
| 10.2 problem+json with stable codes | `TestErrorToProblem`, `TestEveryCatalogCodeIsConsistent`, `TestErrorCodesMatchTheSpecification`, `TestAPI_ErrorResponses` |
| 10.3 status mapping | `TestErrorToProblem`, `TestAPI_ErrorResponses`, `TestRequestTimeout` (503 with `Retry-After`) |
| 10.4 no internal details | `TestErrorToProblem_NeverLeaksInternalDetails`, `TestCreateAccount_Errors`, `TestPanicRecovery`, `TestHealthAndReadiness` |
| 10.5 money as strings, UUIDs, RFC 3339 UTC | `TestCreateTransaction`, `TestCreateAccount`, `TestAPI_AmountsKeepFullPrecision`, `TestPrecision_EndToEnd`, contract validation of every API test |
| 10.6 OpenAPI matches behaviour | `TestSpecificationIsValidOpenAPI`, `TestRoutesMatchTheSpecification`, `TestErrorCodesMatchTheSpecification`, `TestValidatorCatchesDrift`, and the contract check applied to every exchange of the API tests |
| 10.7 maximum body size | `TestBodyLimit`, `TestDecodeJSON_BodyTooLarge`, `TestAPI_ErrorResponses` |

## R11. Authentication

| Criterion | Verified by |
|---|---|
| 11.1 `/v1` requires a valid key | `TestAuthentication`, `TestAPI_RequiresAnAPIKey`, `TestServe_RunsAndStopsOnCancel` |
| 11.2 key from configuration, never logged | `TestLoadServe_Defaults`, `TestSecret_NeverPrintsValue`, `TestLoadServe_ErrorsNeverLeakSecrets`, `TestAccessLog`, `TestRun_ServeRejectsBadConfigurationWithoutLeakingSecrets` |
| 11.3 constant-time comparison | **By construction, not by a timing test:** both values are hashed with SHA-256 and compared with `subtle.ConstantTimeCompare` (`authenticate` in `internal/api/middleware.go`). `TestAuthentication` covers keys of every length and prefix. |
| 11.4 documented as minimal | the README (Security notes, Limitations) and `docs/openapi.yaml` |
| 11.5 health endpoint without a key | `TestHealthAndReadiness`, `TestAuthentication`, `TestServe_RunsAndStopsOnCancel` |

## R12. Health and observability

| Criterion | Verified by |
|---|---|
| 12.1 `/healthz` | `TestHealthAndReadiness`, `TestServe_RunsAndStopsOnCancel` |
| 12.2 `/readyz` follows the database | `TestHealthAndReadiness`, `TestServe_ReadinessFollowsTheDatabase` |
| 12.3 structured logs with request id, method, path, status, duration | `TestAccessLog`, `TestRequestID` |
| 12.4 no secrets or bodies in logs | `TestAccessLog`, `TestRequestBodiesAreNeverLogged`, `TestSecret_NeverPrintsValue` |
| 12.5 graceful shutdown | `TestServe_GracefulShutdownLetsInFlightRequestsFinish`, `TestServe_ShutdownTimeoutCutsOffStuckRequests`, `TestServe_RunsAndStopsOnCancel` |

## R13. Verification

| Criterion | Verified by |
|---|---|
| 13.1 unit tests of the domain | `internal/ledger/*_test.go` |
| 13.2 integration tests on a real PostgreSQL | `test/integration` (testcontainers; `make test` fails without Docker) |
| 13.3 property tests with large amounts | `TestProperty_ApplyPreservesInvariants`, `TestProperty_ReversalRestoresBalances`, `TestProperty_BalancedRequestsAreValid`, `TestNumeric_RoundTripInBothWireFormats` |
| 13.4 1,000 simultaneous transfers | `TestConcurrency_ThousandSimultaneousTransfers` |
| 13.5 50 simultaneous identical requests | `TestConcurrency_IdempotentDuplicatesOverHTTP`, `TestIdempotency_ConcurrentDuplicatesHaveExactlyOneEffect` |
| 13.6 precision above 2^53 and 2^63 | `TestPrecision_EndToEnd`, `TestAPI_AmountsKeepFullPrecision`, `TestExactNumericRoundTrip`, `TestNumeric_RoundTripThroughColumnsAndArrays` |
| 13.7 race detector in CI | `.github/workflows/ci.yml` (job `test`, `go test -race`) |
| 13.8 CI runs lint, tests and the spec check | `.github/workflows/ci.yml`; the spec check is part of the tests. **The workflow has not run on GitHub yet**: it was validated with `actionlint` and its steps were executed locally one by one. |

## R14. Performance evidence

| Criterion | Verified by |
|---|---|
| 14.1 one documented command | `make bench` (`bench/README.md`) |
| 14.2 throughput, p50/p95/p99, ends with reconciliation | `bench/k6/transfers.js` (the run fails if the final deep reconciliation is not clean; checked by making the teardown fail on purpose, exit code 107) |
| 14.3 published with hardware, versions and configuration; not a guarantee | README, section Benchmark; `bench/results/environment.txt` is produced by every run |
| 14.4 no numeric target | none is asserted anywhere |

## R15. Developer experience

| Criterion | Verified by |
|---|---|
| 15.1 one command starts everything, healthy | `make up` (creates `.env` with random secrets, then `docker compose up --build --wait`); `make smoke`; CI job `stack`. Running `docker compose up` directly needs `.env` to exist first (compose says so if it does not). |
| 15.2 default ports 8085 and 15432, configurable | `docker-compose.yml`, `.env.example` (the PostgreSQL port was moved from 55432 because Windows reserved it on the development machine) |
| 15.3 Makefile targets | `make help` |
| 15.4 images and tools pinned | images by tag and digest in `Dockerfile`, `docker-compose.yml`, `internal/testdb`, the k6 image in the `Makefile`; Go modules by `go.sum`; tools by version in the README and CI; Actions by commit SHA |
| 15.5 README contents | `README.md` |
| 15.6 no employer code or data | **Not automated:** a one-off search of the whole tree and git history was done before release (a permanent check would have to name third parties inside the repository). The code was written for this project; the schema is original. |

## R16. Account listing (version 0.2.0; requirements in `.kiro/specs/account-listing`)

| Criterion | Verified by |
|---|---|
| 16.1 newest first, same shape as an account, with the balance | `TestListAccounts_NewestFirstWithTheirBalances`, `TestAPI_ListAccounts` (including "a balance appears in the list"), `TestListAccounts` |
| 16.2 `limit` (default 50, 1 to 200) and `next_cursor` (null on the last page) | `TestListAccounts_PaginatesWithoutGapsOrRepeats`, `TestListAccounts_ExactlyOnePageHasNoCursor`, `TestListAccounts_Validation` |
| 16.3 stable while accounts are created | `TestListAccounts_StableWhileAccountsAreCreated`, `TestListAccounts_PagingWhileAccountsAreBeingCreated`, `TestListAccounts_AccountsWithTheSameTimestampAreOrderedById` |
| 16.4 `code_prefix` is a plain prefix | `TestListAccounts_Filters` (a `_` in the prefix is not a wildcard) |
| 16.5 `currency`, combined with AND | `TestListAccounts_Filters` |
| 16.6 invalid or unknown parameters | `TestListAccounts_Validation`, `TestListAccounts`, `TestAPI_ListAccounts` ("errors"), `TestAccountCursor_RejectsGarbage` |
| 16.7 empty result | `TestListAccounts_Filters` ("no match"), `TestListAccounts` |
| 16.8 key required; in the OpenAPI file; contract-tested | `TestAPI_ListAccounts` ("requires the API key" and the contract check on every exchange), `TestRoutesMatchTheSpecification` |
| 16.9 served by the `(created_at DESC, id DESC)` index | `TestListAccounts_UsesTheCreatedAtIndex` |

The suite also contains many supporting tests (edge cases of parsing, retry timing,
configuration, CLI behaviour, generators) that are not listed here because they
verify implementation details rather than a stated requirement.
