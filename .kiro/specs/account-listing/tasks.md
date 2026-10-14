# Implementation Plan — account listing and release images (ledgerd 0.2.0)

- [x] 33. Listing: query, migration, store
  - Migration `00004` with the index; `ListAccounts` query in sqlc; store wrapper returning domain accounts; cursor encode/decode.
  - Verify: integration tests for ordering (including equal timestamps), paging, filters, `_`/`%` prefixes, and `EXPLAIN`.
  - _Requirements: R16.1–R16.5, R16.7, R16.9_

- [x] 34. Listing: service and API
  - Service validation and `AccountPage`; handler with strict query parameters; `AccountsPage` DTO.
  - Verify: handler tests with the fake ledger; end-to-end API tests; invalid-parameter cases.
  - _Requirements: R16.1–R16.8_

- [x] 35. Listing: OpenAPI, contract tests, stability test
  - Operation and schemas in `docs/openapi.yaml`; route/contract tests pass; a concurrency test creates accounts while a client pages.
  - _Requirements: R16.3, R16.8_

- [x] 36. Release images
  - `deploy/postgres.Dockerfile`; compose reads image names from variables; `release.yml` as designed; README section on the published images.
  - Verify: run the workflow's steps locally up to the push (build both images, smoke test against them); `actionlint`.
  - _Requirements: R17.1–R17.6_

- [x] 37. Documentation, traceability and release 0.2.0
  - README (endpoint table, images), traceability matrix for R16 and R17, ADR if needed, full test run, tag `v0.2.0`, watch the release workflow, record the digests.

Status: released as v0.2.0. The images were published by the release workflow, are public (an anonymous `docker pull` works), and their digests are in the GitHub release notes.
