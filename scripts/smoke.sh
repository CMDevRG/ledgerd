#!/usr/bin/env bash
# End-to-end check of a running stack: create accounts, deposit (twice, to prove
# idempotency), withdraw, overdraw (must be refused), reverse, reconcile.
#
#   scripts/smoke.sh                       # uses .env and http://127.0.0.1:${LEDGERD_PORT:-8085}
#   BASE_URL=http://host:8085 LEDGERD_API_KEY=... scripts/smoke.sh
set -euo pipefail

cd "$(dirname "$0")/.."
if [ -f .env ]; then set -a; . ./.env; set +a; fi
: "${LEDGERD_API_KEY:?LEDGERD_API_KEY is required (run 'make env' first)}"
BASE_URL="${BASE_URL:-http://127.0.0.1:${LEDGERD_PORT:-8085}}"
SUFFIX="$(date +%s)-$RANDOM"

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '  %-52s' "$1"; }
ok()   { echo "ok"; }

# call METHOD PATH [BODY [extra curl args...]] -> prints "<status>\n<body>"
call() {
  local method=$1 path=$2 body=${3:-}
  shift $(( $# >= 3 ? 3 : $# ))
  local args=(-sS -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $LEDGERD_API_KEY" -w '\n%{http_code}')
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' --data "$body"); fi
  curl "${args[@]}" "$@"
}
status() { tail -n1; }
body()   { sed '$d'; }

echo "Smoke test against $BASE_URL"

step "service is ready"
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/readyz")" = 200 ] || fail "/readyz is not 200"
ok

step "requests without the API key are refused"
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/v1/accounts/00000000-0000-0000-0000-000000000001")" = 401 ] || fail "expected 401"
ok

step "create two accounts"
r=$(call POST /v1/accounts "{\"code\":\"smoke-cash-$SUFFIX\",\"currency\":\"USD\",\"type\":\"ASSET\",\"normal_balance\":\"DEBIT\"}")
[ "$(echo "$r" | status)" = 201 ] || fail "create cash: $r"
CASH=$(echo "$r" | body | jq -r .id)
r=$(call POST /v1/accounts "{\"code\":\"smoke-wallet-$SUFFIX\",\"currency\":\"USD\",\"type\":\"LIABILITY\",\"normal_balance\":\"CREDIT\"}")
[ "$(echo "$r" | status)" = 201 ] || fail "create wallet: $r"
WALLET=$(echo "$r" | body | jq -r .id)
ok

tx() { # tx DEBIT CREDIT AMOUNT
  echo "{\"description\":\"smoke\",\"postings\":[{\"account_id\":\"$1\",\"direction\":\"DEBIT\",\"amount\":\"$3\",\"currency\":\"USD\"},{\"account_id\":\"$2\",\"direction\":\"CREDIT\",\"amount\":\"$3\",\"currency\":\"USD\"}]}"
}

step "deposit 100.00"
r=$(call POST /v1/transactions "$(tx "$CASH" "$WALLET" 10000)" -H "Idempotency-Key: smoke-dep-$SUFFIX")
[ "$(echo "$r" | status)" = 201 ] || fail "deposit: $r"
DEP=$(echo "$r" | body | jq -r .id)
ok

step "the same deposit again is a replay, not a second deposit"
r=$(call POST /v1/transactions "$(tx "$CASH" "$WALLET" 10000)" -H "Idempotency-Key: smoke-dep-$SUFFIX" -D /dev/stderr 2>&1 || true)
echo "$r" | grep -qi 'idempotent-replayed: true' || fail "no replay header: $r"
ok

step "balance is 10000, not 20000"
b=$(call GET "/v1/accounts/$WALLET/balance" | body | jq -r .balance)
[ "$b" = 10000 ] || fail "balance $b"
ok

step "withdrawing more than the balance is refused"
r=$(call POST /v1/transactions "$(tx "$WALLET" "$CASH" 10001)" -H "Idempotency-Key: smoke-over-$SUFFIX")
[ "$(echo "$r" | status)" = 422 ] || fail "expected 422: $r"
[ "$(echo "$r" | body | jq -r .code)" = insufficient_funds ] || fail "wrong code: $r"
ok

step "withdraw 25.00"
r=$(call POST /v1/transactions "$(tx "$WALLET" "$CASH" 2500)" -H "Idempotency-Key: smoke-wd-$SUFFIX")
[ "$(echo "$r" | status)" = 201 ] || fail "withdraw: $r"
ok

step "an amount sent as a JSON number is rejected"
num="{\"postings\":[{\"account_id\":\"$CASH\",\"direction\":\"DEBIT\",\"amount\":5,\"currency\":\"USD\"},{\"account_id\":\"$WALLET\",\"direction\":\"CREDIT\",\"amount\":\"5\",\"currency\":\"USD\"}]}"
[ "$(call POST /v1/transactions "$num" -H "Idempotency-Key: smoke-num-$SUFFIX" | status)" = 422 ] || fail "expected 422"
ok

step "reverse the withdrawal"
w=$(call GET "/v1/accounts/$WALLET/postings?limit=1" | body | jq -r '.postings[0].transaction_id')
r=$(call POST "/v1/transactions/$w/reverse" "" -H "Idempotency-Key: smoke-rev-$SUFFIX")
[ "$(echo "$r" | status)" = 201 ] || fail "reverse: $r"
b=$(call GET "/v1/accounts/$WALLET/balance" | body | jq -r .balance)
[ "$b" = 10000 ] || fail "balance after reversal $b"
ok

step "history lists three postings, newest first"
n=$(call GET "/v1/accounts/$WALLET/postings" | body | jq '.postings | length')
[ "$n" = 3 ] || fail "$n postings"
ok

step "reconciliation (deep) reports a consistent ledger"
r=$(call POST "/v1/reconciliations?deep=true" "")
[ "$(echo "$r" | body | jq -r .ok)" = true ] || fail "reconciliation: $r"
ok

: "$DEP"
echo "All checks passed."
