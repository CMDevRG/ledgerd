package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAPI_RequiresAnAPIKey(t *testing.T) {
	a := newAPIEnv(t)
	for _, path := range []string{"/v1/accounts/00000000-0000-0000-0000-000000000001", "/v1/transactions/00000000-0000-0000-0000-000000000001", "/v1/unknown"} {
		r := a.do(t, "GET", path, "", "Authorization", "")
		a.mustStatus(t, r, 401)
		if r.json(t)["code"] != "unauthorized" {
			t.Errorf("%s: %s", path, r.Body)
		}
	}
	a.mustStatus(t, a.do(t, "GET", "/v1/accounts/00000000-0000-0000-0000-000000000001", "", "Authorization", "Bearer wrong"), 401)
}

func TestAPI_FullLifecycle(t *testing.T) {
	a := newAPIEnv(t)

	cash := a.createAccount(t, "bank-cash", "USD", "ASSET", "DEBIT", false)
	wallet := a.createAccount(t, "user-wallet", "USD", "LIABILITY", "CREDIT", false)

	// ---- deposit, then the same request again (retry) ----
	deposit := txBody(cash, wallet, "10000", "USD")
	first := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", deposit, "Idempotency-Key", "dep-1"), 201)
	if first.Header.Get("Idempotent-Replayed") != "" {
		t.Error("the first call is not a replay")
	}
	txID := first.json(t)["id"].(string)
	if loc := first.Header.Get("Location"); loc != "/v1/transactions/"+txID {
		t.Errorf("Location = %q", loc)
	}

	replay := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", deposit, "Idempotency-Key", "dep-1"), 201)
	if replay.Header.Get("Idempotent-Replayed") != "true" || replay.json(t)["id"] != txID {
		t.Errorf("replay: header=%q id=%v", replay.Header.Get("Idempotent-Replayed"), replay.json(t)["id"])
	}
	// The same request spelled differently is still the same request.
	reformatted := `{"postings":[{"currency":"USD","amount":"10000","direction":"CREDIT","account_id":"` + wallet + `"},` +
		`{"currency":"USD","amount":"10000","direction":"DEBIT","account_id":"` + cash + `"}],"description":"test"}`
	if different := a.do(t, "POST", "/v1/transactions", reformatted, "Idempotency-Key", "dep-1"); different.Status != 422 {
		t.Errorf("swapped posting order is a different request: %d", different.Status)
	}

	reuse := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(cash, wallet, "999", "USD"), "Idempotency-Key", "dep-1"), 422)
	if reuse.json(t)["code"] != "idempotency_key_reuse" {
		t.Errorf("%s", reuse.Body)
	}
	missing := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", deposit), 422)
	if missing.json(t)["code"] != "validation_error" {
		t.Errorf("%s", missing.Body)
	}

	if n := a.count(t, "transactions"); n != 1 {
		t.Fatalf("%d transactions after retries, want 1", n)
	}

	// ---- balances ----
	acct := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet, ""), 200).json(t)
	if acct["balance"] != "10000" || acct["last_posting_at"] == nil {
		t.Errorf("wallet: %v", acct)
	}
	bal := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+cash+"/balance", ""), 200).json(t)
	if bal["balance"] != "10000" || bal["as_of"] != nil {
		t.Errorf("cash balance: %v", bal)
	}

	// ---- overdraft and unbalanced requests are refused with explanations ----
	over := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(wallet, cash, "10001", "USD"), "Idempotency-Key", "w-1"), 422).json(t)
	// Both accounts are protected and both would end at -1, so the one named is
	// whichever has the lower id: either is a correct answer.
	detail, _ := over["detail"].(string)
	if over["code"] != "insufficient_funds" || (!strings.Contains(detail, wallet) && !strings.Contains(detail, cash)) || !strings.Contains(detail, "overdrawn by 1") {
		t.Errorf("overdraft: %v", over)
	}
	unbalanced := `{"postings":[` +
		`{"account_id":"` + cash + `","direction":"DEBIT","amount":"10000","currency":"USD"},` +
		`{"account_id":"` + wallet + `","direction":"CREDIT","amount":"9999","currency":"USD"}]}`
	ub := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", unbalanced, "Idempotency-Key", "u-1"), 422).json(t)
	if ub["code"] != "unbalanced_transaction" {
		t.Errorf("unbalanced: %v", ub)
	}
	// Refused requests did not consume their keys: a corrected request with key w-1 works.
	a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(wallet, cash, "2500", "USD"), "Idempotency-Key", "w-1"), 201)
	a.requireBalance(t, mustParseUUID(t, wallet), "7500")

	// ---- reversal ----
	rev := a.mustStatus(t, a.do(t, "POST", "/v1/transactions/"+txID+"/reverse", "", "Idempotency-Key", "rev-1"), 422) // wallet only has 7500 left
	if rev.json(t)["code"] != "insufficient_funds" {
		t.Errorf("reversing a partly spent deposit: %s", rev.Body)
	}
	a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(cash, wallet, "2500", "USD"), "Idempotency-Key", "dep-2"), 201)
	reversed := a.mustStatus(t, a.do(t, "POST", "/v1/transactions/"+txID+"/reverse", "", "Idempotency-Key", "rev-2"), 201).json(t)
	if reversed["reverses_id"] != txID {
		t.Errorf("reverses_id = %v", reversed["reverses_id"])
	}
	again := a.mustStatus(t, a.do(t, "POST", "/v1/transactions/"+txID+"/reverse", "", "Idempotency-Key", "rev-3"), 409).json(t)
	if again["code"] != "already_reversed" {
		t.Errorf("%v", again)
	}
	replayed := a.mustStatus(t, a.do(t, "POST", "/v1/transactions/"+txID+"/reverse", "", "Idempotency-Key", "rev-2"), 201)
	if replayed.Header.Get("Idempotent-Replayed") != "true" {
		t.Error("retrying the reversal with its own key is a replay")
	}

	got := a.mustStatus(t, a.do(t, "GET", "/v1/transactions/"+txID, ""), 200).json(t)
	if got["id"] != txID || len(got["postings"].([]any)) != 2 {
		t.Errorf("GET transaction: %v", got)
	}

	// ---- reconciliation ----
	for _, q := range []string{"", "?deep=true"} {
		rep := a.mustStatus(t, a.do(t, "POST", "/v1/reconciliations"+q, ""), 200).json(t)
		if rep["ok"] != true || rep["accounts_checked"].(float64) != 2 {
			t.Errorf("reconciliation%s: %v", q, rep)
		}
	}
}

func TestAPI_AmountsKeepFullPrecision(t *testing.T) {
	a := newAPIEnv(t)
	treasury := a.createAccount(t, "treasury", "ETH", "ASSET", "DEBIT", false)
	wallet := a.createAccount(t, "eth-wallet", "ETH", "LIABILITY", "CREDIT", false)

	const amount = "123456789012345678901234567890" // ~1.2×10^29, far beyond int64 and float64
	r := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, amount, "ETH"), "Idempotency-Key", "big-1"), 201)
	if !strings.Contains(string(r.Body), `"amount":"`+amount+`"`) || !strings.Contains(string(r.Body), `"balance_after":"`+amount+`"`) {
		t.Errorf("the response must carry the exact amount as a string:\n%s", r.Body)
	}

	bal := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance", ""), 200).json(t)
	if bal["balance"] != amount {
		t.Errorf("balance = %v, want %s", bal["balance"], amount)
	}

	// 2^53 + 1, the first integer a float64 cannot hold.
	a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, "9007199254740993", "ETH"), "Idempotency-Key", "big-2"), 201)
	bal = a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance", ""), 200).json(t)
	if bal["balance"] != "123456789012354686100489308883" { // amount + 2^53 + 1
		t.Errorf("balance = %v", bal["balance"])
	}

	// A JSON number is refused, even a small one: it may already have been rounded.
	number := `{"postings":[{"account_id":"` + treasury + `","direction":"DEBIT","amount":10,"currency":"ETH"},` +
		`{"account_id":"` + wallet + `","direction":"CREDIT","amount":"10","currency":"ETH"}]}`
	rej := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", number, "Idempotency-Key", "num-1"), 422).json(t)
	if rej["code"] != "validation_error" {
		t.Errorf("%v", rej)
	}
}

func TestAPI_BalanceAsOfAndPostingPagination(t *testing.T) {
	a := newAPIEnv(t)
	cash := a.createAccount(t, "cash", "USD", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "USD", "LIABILITY", "CREDIT", true)

	var created []string
	for i := 1; i <= 5; i++ {
		r := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(cash, wallet, fmt.Sprint(i*10), "USD"), "Idempotency-Key", fmt.Sprintf("k-%d", i)), 201)
		created = append(created, r.json(t)["created_at"].(string))
		time.Sleep(3 * time.Millisecond) // distinct timestamps
	}

	// Balance after transaction i is 10+20+...+10i.
	want := []string{"10", "30", "60", "100", "150"}
	for i, at := range created {
		bal := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance?as_of="+at, ""), 200).json(t)
		if bal["balance"] != want[i] || bal["as_of"] != at {
			t.Errorf("as_of %s: balance=%v as_of=%v, want %s", at, bal["balance"], bal["as_of"], want[i])
		}
	}
	before := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance?as_of=2000-01-01T00:00:00Z", ""), 200).json(t)
	if before["balance"] != "0" || before["last_posting_at"] != nil {
		t.Errorf("before the first posting: %v", before)
	}
	future := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance?as_of=2999-01-01T00:00:00Z", ""), 200).json(t)
	if future["balance"] != "150" || future["as_of"] != nil {
		t.Errorf("a future instant gives the current balance: %v", future)
	}

	// Page through the history two at a time.
	var seen []string
	path := "/v1/accounts/" + wallet + "/postings?limit=2"
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		page := a.mustStatus(t, a.do(t, "GET", path, ""), 200).json(t)
		for _, p := range page["postings"].([]any) {
			seen = append(seen, p.(map[string]any)["balance_after"].(string))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/v1/accounts/" + wallet + "/postings?limit=2&cursor=" + next
	}
	if strings.Join(seen, ",") != "150,100,60,30,10" {
		t.Errorf("history (newest first) = %v", seen)
	}
}

func TestAPI_ErrorResponses(t *testing.T) {
	a := newAPIEnv(t)
	const ghost = "00000000-0000-0000-0000-000000000001"

	tests := []struct {
		name         string
		method, path string
		body         string
		headers      []string
		status       int
		code         string
	}{
		{"unknown account", "GET", "/v1/accounts/" + ghost, "", nil, 404, "not_found"},
		{"unknown transaction", "GET", "/v1/transactions/" + ghost, "", nil, 404, "not_found"},
		{"balance of an unknown account", "GET", "/v1/accounts/" + ghost + "/balance", "", nil, 404, "not_found"},
		{"postings of an unknown account", "GET", "/v1/accounts/" + ghost + "/postings", "", nil, 404, "not_found"},
		{"reverse an unknown transaction", "POST", "/v1/transactions/" + ghost + "/reverse", "", []string{"Idempotency-Key", "x"}, 404, "not_found"},
		{"id is not a UUID", "GET", "/v1/accounts/42", "", nil, 400, "malformed_request"},
		{"unknown endpoint", "GET", "/v1/nothing-here", "", nil, 404, "not_found"},
		{"wrong method", "DELETE", "/v1/accounts/" + ghost, "", nil, 405, "method_not_allowed"},
		{"invalid JSON", "POST", "/v1/accounts", `{"code":`, nil, 400, "malformed_request"},
		{"unknown field", "POST", "/v1/accounts", `{"cod":"x"}`, nil, 400, "malformed_request"},
		{"invalid account", "POST", "/v1/accounts", `{"code":"","currency":"usd","type":"X","normal_balance":"Y"}`, nil, 422, "validation_error"},
		{"transaction without postings", "POST", "/v1/transactions", `{"postings":[]}`, []string{"Idempotency-Key", "k"}, 422, "validation_error"},
		{"bad as_of", "GET", "/v1/accounts/" + ghost + "/balance?as_of=tomorrow", "", nil, 422, "validation_error"},
		{"bad limit", "GET", "/v1/accounts/" + ghost + "/postings?limit=0", "", nil, 422, "validation_error"},
		{"unknown query parameter", "GET", "/v1/accounts/" + ghost + "/postings?offset=5", "", nil, 400, "malformed_request"},
		{"wrong content type", "POST", "/v1/accounts", `{}`, []string{"Content-Type", "text/plain"}, 415, "unsupported_media_type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := a.do(t, tc.method, tc.path, tc.body, tc.headers...)
			if r.Status != tc.status {
				t.Fatalf("status = %d, want %d\n%s", r.Status, tc.status, r.Body)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			p := r.json(t)
			if p["code"] != tc.code || p["status"].(float64) != float64(tc.status) || p["request_id"] == "" || p["instance"] == "" {
				t.Errorf("problem = %v", p)
			}
		})
	}

	t.Run("a duplicate account code", func(t *testing.T) {
		a.createAccount(t, "dup", "USD", "ASSET", "DEBIT", false)
		r := a.do(t, "POST", "/v1/accounts", `{"code":"dup","currency":"USD","type":"ASSET","normal_balance":"DEBIT"}`)
		if r.Status != 409 || r.json(t)["code"] != "account_code_exists" {
			t.Errorf("%d %s", r.Status, r.Body)
		}
	})
	t.Run("an oversized body", func(t *testing.T) {
		r := a.do(t, "POST", "/v1/accounts", `{"code":"`+strings.Repeat("a", 2<<20)+`"}`)
		if r.Status != 413 || r.json(t)["code"] != "payload_too_large" {
			t.Errorf("%d %.200s", r.Status, r.Body)
		}
	})
}
