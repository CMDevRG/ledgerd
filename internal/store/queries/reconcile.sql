-- name: CountAccounts :one
SELECT count(*) FROM accounts;

-- FindBalanceMismatches lists accounts whose cached balance differs from the
-- balance recomputed from their postings. A posting adds to the balance when
-- its direction is the account's normal side and subtracts otherwise.
-- name: FindBalanceMismatches :many
SELECT a.id,
       a.code,
       a.currency,
       a.balance AS stored,
       COALESCE(SUM(CASE WHEN p.direction = a.normal_balance THEN p.amount ELSE -p.amount END), 0)::numeric AS expected
FROM accounts a
LEFT JOIN postings p ON p.account_id = a.id
GROUP BY a.id, a.code, a.currency, a.balance
HAVING a.balance <> COALESCE(SUM(CASE WHEN p.direction = a.normal_balance THEN p.amount ELSE -p.amount END), 0)
ORDER BY a.id;

-- name: LedgerTotalsByCurrency :many
SELECT currency,
       COALESCE(SUM(amount) FILTER (WHERE direction = 'DEBIT'), 0)::numeric  AS debits,
       COALESCE(SUM(amount) FILTER (WHERE direction = 'CREDIT'), 0)::numeric AS credits
FROM postings
GROUP BY currency
ORDER BY currency;

-- FindBalanceChainBreaks checks every posting's balance_after against the
-- previous posting of the same account plus its own effect.
-- name: FindBalanceChainBreaks :many
SELECT chain.id, chain.account_id, chain.balance_after, chain.expected
FROM (
  SELECT p.id,
         p.account_id,
         p.balance_after,
         (COALESCE(lag(p.balance_after) OVER w, 0)
           + CASE WHEN p.direction = a.normal_balance THEN p.amount ELSE -p.amount END)::numeric AS expected
  FROM postings p
  JOIN accounts a ON a.id = p.account_id
  WINDOW w AS (PARTITION BY p.account_id ORDER BY p.id)
) AS chain
WHERE chain.balance_after <> chain.expected
ORDER BY chain.id;
