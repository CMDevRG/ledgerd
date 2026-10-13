-- InsertPosting is executed as a pgx batch (one network round trip for all
-- postings of a transaction). Batch items run in order, so identity ids are
-- assigned in request order and the last posting of an account always has the
-- highest id.
-- name: InsertPosting :batchone
INSERT INTO postings (transaction_id, account_id, currency, direction, amount, balance_after, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListTransactionPostings :many
SELECT * FROM postings WHERE transaction_id = $1 ORDER BY id;

-- ListAccountPostings returns an account's postings, newest first. Passing a
-- cursor (the id of the last posting already seen) returns the next page.
-- name: ListAccountPostings :many
SELECT * FROM postings
WHERE account_id = @account_id
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT @page_size;

-- GetBalanceAsOf returns the balance right after the latest posting recorded at
-- or before the given instant, together with that posting's timestamp. No row
-- means the account had no postings yet.
--
-- recorded_at never decreases along an account's posting ids (the service
-- guarantees it while holding the account lock), so ordering by
-- (recorded_at, id) gives the same row as ordering by id, and lets PostgreSQL
-- walk the (account_id, recorded_at, id) index straight to the answer.
-- name: GetBalanceAsOf :one
SELECT balance_after, recorded_at FROM postings
WHERE account_id = @account_id AND recorded_at <= @as_of::timestamptz
ORDER BY recorded_at DESC, id DESC
LIMIT 1;
