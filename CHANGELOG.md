# Changelog

Versions follow [Semantic Versioning](https://semver.org). Published versions are never changed.

## 0.2.0

### Added

- `GET /v1/accounts`: list accounts, newest first, with keyset pagination and the optional
  filters `code_prefix` (a plain prefix; `_` is not a wildcard) and `currency`. Each account
  includes its current balance. Pages are stable while accounts are being created.
- Migration `00004`: an index on `accounts (created_at DESC, id DESC)` that serves the listing.
- Release images on GitHub Container Registry, tested exactly as published and never overwritten:
  `ghcr.io/cmdevrg/ledgerd` and `ghcr.io/cmdevrg/ledgerd-postgres`, with OCI labels (version,
  commit, source).
- `docker-compose.images.yml`, `make images` and `make smoke-images` to run the stack from them.
- A `release` workflow: tag, test, build, smoke-test the images, push, create the release with the digests.

### Compatibility

The 0.1.0 API is unchanged. The migration is additive.

## 0.1.0

First release: balanced double-entry transactions, exact integer money, idempotent writes,
overdraft protection under concurrency, reversals, historical balances, keyset-paginated
history, snapshot reconciliation, an OpenAPI contract verified by the tests, a Docker Compose
stack, a k6 benchmark, and CI.
