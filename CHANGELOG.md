# Changelog

Versions follow [Semantic Versioning](https://semver.org). Published versions are never changed.

## 0.2.1

### Fixed

- `docker-compose.yml`: on a fresh volume the stack failed to start about one time in six
  (2 of 12 starts measured; none in 24 after the fix). The PostgreSQL image runs its init scripts
  on a temporary server that listens only on a Unix socket, then restarts as the real server. The
  health check asked over the socket, so it passed during the first phase; `migrate` then started,
  connected over TCP and was refused. The check now asks over TCP (`pg_isready -h 127.0.0.1`), so
  it passes only when the real server is listening. A test (`deploy/compose_test.go`) fails if the
  check goes back to the socket.

### Compatibility

Nothing else changed: the API, the migrations and the code are those of 0.2.0. The 0.2.1 images
contain the same program and database setup as 0.2.0 (only their version label differs), so there
is no need to move to them if you start ledgerd with your own compose file. Anyone who starts it
with this repository's `docker-compose.yml` (`make up`, `make smoke-images`) gets the fix.

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
