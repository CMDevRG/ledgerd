-- name: InsertTransaction :one
INSERT INTO transactions (description, reverses_id, created_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- GetReversalOf returns the transaction that reverses the given one, if any.
-- name: GetReversalOf :one
SELECT * FROM transactions WHERE reverses_id = $1;
