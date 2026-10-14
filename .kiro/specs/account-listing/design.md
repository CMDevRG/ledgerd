# Design Document — account listing and release images (ledgerd 0.2.0)

## 1. Listing

### Query

```sql
-- name: ListAccounts :many
SELECT * FROM accounts
WHERE (sqlc.narg('code_prefix')::text IS NULL OR starts_with(code, sqlc.narg('code_prefix')::text))
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;
```

- `starts_with` is a plain string function: no `LIKE` wildcards, so `_` and `%` need no escaping (the account-code alphabet contains `_`, which `LIKE` would treat as "any character").
- The ordering key `(created_at, id)` never changes for a row, so a cursor keeps pointing at the same place however many accounts are added. An account created while someone pages can only appear in a region the reader has not reached or one already passed, never cause a skip or a repeat of an existing one.
- `created_at` is set by the database when the row is inserted, which is when the transaction started; two accounts can share it, hence the `id` tie-break.
- Migration `00004_account_listing.sql`: `CREATE INDEX accounts_created_idx ON accounts (created_at DESC, id DESC)` (and its `DOWN`).

### Cursor

`base64url("v1:" + created_at as microseconds since the Unix epoch + ":" + id)`; decoding is strict (canonical form only), like the postings cursor. The page query fetches `limit + 1` rows to know whether a next page exists.

### Layers

- `store`: `ListAccounts(ctx, filter, after, limit)`; the generated query is wrapped to return domain accounts.
- `service`: `ListAccounts(ctx, ListFilter{CodePrefix, Currency, Cursor, Limit})` validates (reusing the account-code and asset-code validators), returns `AccountPage{Accounts, NextCursor}`.
- `api`: `GET /v1/accounts` with strict query parsing (the existing `query` helper), response `{ "accounts": [ ... ], "next_cursor": null | "..." }`.
- `docs/openapi.yaml`: the operation, parameters, and an `AccountsPage` schema; the route list test and contract validation then cover it automatically.

### Tests

Unit: cursor round trip and garbage; parameter validation. Integration: ordering with equal timestamps; paging through 120 accounts; stability while accounts are inserted between pages; both filters and their combination; `code_prefix` with `_` and `%` in the prefix; empty result; invalid parameters; `EXPLAIN` shows the index; contract validation of every response.

## 2. Release images

- `Dockerfile` already builds the service (`ghcr.io/cmdevrg/ledgerd`). A new `deploy/postgres.Dockerfile` is `FROM postgres:16-alpine@sha256:…` plus `COPY deploy/initdb/ /docker-entrypoint-initdb.d/`, so the role-creation script ships inside the database image (`ghcr.io/cmdevrg/ledgerd-postgres`).
- A new workflow `.github/workflows/release.yml`, triggered by tags `v*.*.*`, with `packages: write` only in the job that pushes:
  1. run the full test suite (same as CI);
  2. build both images locally (`make images`) and run the compose smoke test with `LEDGERD_IMAGE`/`LEDGERD_POSTGRES_IMAGE` pointing at them (`make smoke-images`);
  3. `docker push` those same images (the bytes that passed the smoke test), exact version tags only (`<version>`): a moving tag such as `latest` or `0.2` would contradict the immutability rule. Registry-side provenance attestations are not produced, a deliberate trade for pushing exactly what was tested;
  4. create the GitHub release with the digests in the notes.
- `docker-compose.yml` reads the two image names from variables, defaulting to a local build, so developers keep building from source while the console pulls the published, pinned images.
- Tags are immutable by policy: the workflow fails if the tag already exists in the registry.

## 3. Compatibility

0.2.0 only adds an endpoint and an index; the 0.1.0 API is unchanged. The migration is additive.
