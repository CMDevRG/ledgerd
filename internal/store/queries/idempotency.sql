-- ClaimIdempotencyKey inserts the key. If another transaction has the same key
-- uncommitted, this statement waits until that transaction finishes; if the key
-- already exists the statement returns no row.
-- name: ClaimIdempotencyKey :one
INSERT INTO idempotency_keys (key, request_hash, response_status, response_body)
VALUES ($1, $2, 0, '{}'::jsonb)
ON CONFLICT (key) DO NOTHING
RETURNING key;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE key = $1;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys
SET response_status = $2, response_body = $3, transaction_id = $4
WHERE key = $1;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < $1;
