# Requirements Document — account listing and release images (ledgerd 0.2.0)

## Introduction

ledgerd 0.1.0 can create an account and read one whose id is already known, but it
cannot say which accounts exist. The [ledgerd console](https://github.com/CMDevRG/ledgerd-console)
needs that to be usable, and so does anyone exploring the API by hand. This change also
publishes release images so that other projects (the console) can start ledgerd without
building it.

This is an addition to [ledger-core](../ledger-core/requirements.md); requirement numbers
continue from there. EARS notation, as before.

---

### Requirement 16: Account listing

**User Story:** As an API client, I want to list accounts in pages, so that I can discover what exists.

#### Acceptance Criteria

1. WHEN a client requests `GET /v1/accounts` THEN the system SHALL return accounts newest first (by creation time, then by id), each in the same shape as `GET /v1/accounts/{id}`, including its current balance.
2. The system SHALL paginate with an opaque cursor and a `limit` parameter (default 50, minimum 1, maximum 200); the response SHALL contain `next_cursor`, which is `null` on the last page.
3. WHEN accounts are created between page requests THEN pagination SHALL NOT skip or repeat accounts that existed when the first page was read.
4. WHERE the optional `code_prefix` parameter is given (1 to 64 characters of the account-code alphabet) THEN the system SHALL return only accounts whose code starts with it; the characters `%` and `_` SHALL have no special meaning.
5. WHERE the optional `currency` parameter is given THEN the system SHALL return only accounts of that asset code; the two filters SHALL combine with AND.
6. WHEN `limit`, `cursor`, `code_prefix` or `currency` is invalid THEN the system SHALL reject the request with a validation error naming the parameter; an unknown or repeated parameter SHALL be rejected as a malformed request.
7. WHEN no account matches THEN the system SHALL return an empty list and a `null` cursor.
8. The endpoint SHALL require the API key like every other `/v1` endpoint, SHALL be described in `docs/openapi.yaml`, and SHALL be covered by the contract tests.
9. The query SHALL be served by an index on `(created_at DESC, id DESC)`; a test SHALL show that the plan uses it for an unfiltered listing.

### Requirement 17: Release images

**User Story:** As a developer of another project, I want ready-made ledgerd images, so that I can run ledgerd without building it.

#### Acceptance Criteria

1. WHEN a version tag (`vMAJOR.MINOR.PATCH`) is pushed THEN continuous integration SHALL build and publish two images to GitHub Container Registry: `ghcr.io/cmdevrg/ledgerd:<version>` (the service; also runs `migrate`) and `ghcr.io/cmdevrg/ledgerd-postgres:<version>` (PostgreSQL 16 with the role-creation script of `deploy/initdb` built in).
2. Before publishing, the workflow SHALL run the test suite and the compose smoke test against the images it is about to publish; a failure SHALL prevent publishing.
3. The images SHALL be reproducible from the tagged commit, built from the pinned base images, run as a non-root user, and carry OCI labels with the version, commit and source repository.
4. The release notes SHALL list the image digests.
5. Published tags SHALL NOT be overwritten.
6. The README SHALL document how to run the published images.
