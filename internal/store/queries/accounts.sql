-- name: CreateAccount :one
INSERT INTO accounts (code, currency, type, normal_balance, allow_negative)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- LockAccounts takes a row lock on every requested account that exists, in
-- ascending id order. All writers lock in this same global order, which is what
-- makes deadlocks between concurrent transactions impossible.
-- name: LockAccounts :many
SELECT * FROM accounts
WHERE id = ANY(@ids::uuid[])
ORDER BY id
FOR UPDATE;

-- UpdateAccountBalances sets the new balance of several accounts in one round trip.
-- name: UpdateAccountBalances :exec
UPDATE accounts AS a
SET balance = u.balance, last_posting_at = @recorded_at::timestamptz
FROM (
  SELECT unnest(@ids::uuid[]) AS id, unnest(@balances::numeric[]) AS balance
) AS u
WHERE a.id = u.id;

-- ListAccounts returns accounts newest first. The filters are optional; the cursor
-- (the created_at and id of the last account already seen) selects the next page.
-- starts_with is a plain string function, so '_' and '%' in a prefix are ordinary
-- characters (LIKE would treat '_' as "any character", and '_' is legal in a code).
-- name: ListAccounts :many
SELECT * FROM accounts
WHERE (sqlc.narg('code_prefix')::text IS NULL OR starts_with(code, sqlc.narg('code_prefix')::text))
  AND (sqlc.narg('currency')::text IS NULL OR currency = sqlc.narg('currency')::text)
  AND (sqlc.narg('after_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;
