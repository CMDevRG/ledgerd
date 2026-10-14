-- +goose Up

-- Serves GET /v1/accounts: newest first, keyset-paginated on (created_at, id).
-- Neither column ever changes for a row, so a cursor keeps pointing at the same place.
CREATE INDEX accounts_created_idx ON accounts (created_at DESC, id DESC);

-- +goose Down
DROP INDEX accounts_created_idx;
