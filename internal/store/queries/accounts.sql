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
