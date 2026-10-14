package integration

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// randomAmount draws a uniformly distributed amount in [1, 10^30].
func randomAmount(rng *rand.Rand) *big.Int {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
	b := make([]byte, 20)
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	v := new(big.Int).SetBytes(b)
	v.Mod(v, limit)
	return v.Add(v, big.NewInt(1))
}

// Every number that crosses the HTTP boundary is a string of decimal digits.
// This test sends amounts at and beyond the limits of float64 (2^53), int64
// (2^63) and uint64 (2^64), up to the largest allowed posting (10^30), and checks that
// every place they come back (the write response, the transaction, the account,
// the current and historical balance, the posting history and the
// reconciliation totals) shows exactly the digits a big-integer oracle expects.
func TestPrecision_EndToEnd(t *testing.T) {
	a := newAPIEnv(t)
	rng := seedFor(t)

	treasury := a.createAccount(t, "treasury", "ETH", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "ETH", "LIABILITY", "CREDIT", false)

	amounts := []*big.Int{
		big.NewInt(1),
		big.NewInt(1 << 53), // exactly representable as float64
		new(big.Int).Add(big.NewInt(1<<53), big.NewInt(1)),                                     // first integer float64 cannot hold
		big.NewInt(9223372036854775807),                                                        // int64 max
		new(big.Int).Add(big.NewInt(9223372036854775807), big.NewInt(1)),                       // int64 max + 1
		new(big.Int).Lsh(big.NewInt(1), 64),                                                    // 2^64
		new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil),                                  // 1 ETH in wei
		new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil), big.NewInt(1)), // 10^30 - 1
		new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil),                                  // 10^30: the largest allowed posting
	}
	for i := 0; i < 8; i++ {
		amounts = append(amounts, randomAmount(rng))
	}

	type step struct {
		txID      string
		createdAt string
		amount    string
		balance   string // wallet balance right after this step
	}
	var steps []step
	balance, debits := new(big.Int), new(big.Int)

	for i, amount := range amounts {
		balance.Add(balance, amount)
		debits.Add(debits, amount)
		r := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, amount.String(), "ETH"), "Idempotency-Key", fmt.Sprintf("p-%d", i)), 201)
		tx := r.json(t)

		for _, p := range tx["postings"].([]any) {
			posting := p.(map[string]any)
			if posting["amount"] != amount.String() {
				t.Errorf("step %d: posting amount %v, want %s", i, posting["amount"], amount)
			}
			if posting["account_id"] == wallet && posting["balance_after"] != balance.String() {
				t.Errorf("step %d: wallet balance_after %v, want %s", i, posting["balance_after"], balance)
			}
		}
		steps = append(steps, step{tx["id"].(string), tx["created_at"].(string), amount.String(), balance.String()})

		got := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet, ""), 200).json(t)
		if got["balance"] != balance.String() {
			t.Errorf("step %d: account balance %v, want %s", i, got["balance"], balance)
		}
		bal := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance", ""), 200).json(t)
		if bal["balance"] != balance.String() {
			t.Errorf("step %d: balance endpoint %v, want %s", i, bal["balance"], balance)
		}
		time.Sleep(2 * time.Millisecond) // give every transaction its own timestamp
	}
	if balance.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)) <= 0 {
		t.Fatal("the scenario must push a balance beyond 10^30 to be meaningful")
	}

	t.Run("each transaction reads back unchanged", func(t *testing.T) {
		for i, s := range steps {
			tx := a.mustStatus(t, a.do(t, "GET", "/v1/transactions/"+s.txID, ""), 200).json(t)
			for _, p := range tx["postings"].([]any) {
				if p.(map[string]any)["amount"] != s.amount {
					t.Errorf("step %d: stored amount %v, want %s", i, p.(map[string]any)["amount"], s.amount)
				}
			}
		}
	})

	t.Run("historical balances", func(t *testing.T) {
		for i, s := range steps {
			bal := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+wallet+"/balance?as_of="+s.createdAt, ""), 200).json(t)
			if bal["balance"] != s.balance {
				t.Errorf("step %d: balance as of %s is %v, want %s", i, s.createdAt, bal["balance"], s.balance)
			}
		}
	})

	t.Run("posting history, newest first", func(t *testing.T) {
		var gotBalances, gotAmounts []string
		path := "/v1/accounts/" + wallet + "/postings?limit=4"
		for pages := 0; pages < 10; pages++ {
			page := a.mustStatus(t, a.do(t, "GET", path, ""), 200).json(t)
			for _, p := range page["postings"].([]any) {
				posting := p.(map[string]any)
				gotBalances = append(gotBalances, posting["balance_after"].(string))
				gotAmounts = append(gotAmounts, posting["amount"].(string))
			}
			next, _ := page["next_cursor"].(string)
			if next == "" {
				break
			}
			path = "/v1/accounts/" + wallet + "/postings?limit=4&cursor=" + next
		}
		for i := range steps {
			s := steps[len(steps)-1-i]
			if i >= len(gotBalances) || gotBalances[i] != s.balance || gotAmounts[i] != s.amount {
				t.Fatalf("history position %d: got (%v, %v), want (%s, %s)", i, at(gotAmounts, i), at(gotBalances, i), s.amount, s.balance)
			}
		}
		if len(gotBalances) != len(steps) {
			t.Errorf("%d postings in the history, want %d", len(gotBalances), len(steps))
		}
	})

	t.Run("reconciliation totals are exact", func(t *testing.T) {
		rep := a.mustStatus(t, a.do(t, "POST", "/v1/reconciliations?deep=true", ""), 200).json(t)
		cur := rep["currencies"].([]any)[0].(map[string]any)
		if rep["ok"] != true || cur["debits"] != debits.String() || cur["credits"] != debits.String() || cur["balanced"] != true {
			t.Errorf("report: %v (want debits = credits = %s)", rep, debits)
		}
	})

	t.Run("undoing everything brings both accounts back to exactly zero", func(t *testing.T) {
		for i := len(steps) - 1; i >= 0; i-- {
			a.mustStatus(t, a.do(t, "POST", "/v1/transactions/"+steps[i].txID+"/reverse", "", "Idempotency-Key", fmt.Sprintf("undo-%d", i)), 201)
		}
		for _, id := range []string{wallet, treasury} {
			if got := a.mustStatus(t, a.do(t, "GET", "/v1/accounts/"+id, ""), 200).json(t)["balance"]; got != "0" {
				t.Errorf("account %s ends at %v, want 0", id, got)
			}
		}
		rep := a.mustStatus(t, a.do(t, "POST", "/v1/reconciliations?deep=true", ""), 200).json(t)
		cur := rep["currencies"].([]any)[0].(map[string]any)
		twice := new(big.Int).Mul(debits, big.NewInt(2))
		if rep["ok"] != true || cur["debits"] != twice.String() || cur["credits"] != twice.String() {
			t.Errorf("after the reversals: %v (want debits = credits = %s)", rep, twice)
		}
	})
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<missing>"
}

// The limits are exact: 10^30 is accepted, one more is not.
func TestPrecision_TheLimitIsExact(t *testing.T) {
	a := newAPIEnv(t)
	treasury := a.createAccount(t, "treasury", "ETH", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "ETH", "LIABILITY", "CREDIT", true)

	max := "1000000000000000000000000000000"
	a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, max, "ETH"), "Idempotency-Key", "max"), 201)

	over := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, max+"1", "ETH"), "Idempotency-Key", "over-digits"), 422).json(t)
	if over["code"] != "validation_error" {
		t.Errorf("%v", over)
	}
	oneMore := strings.TrimSuffix(max, "0") + "1" // 10^30 + 1
	if p := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(treasury, wallet, oneMore, "ETH"), "Idempotency-Key", "over-by-one"), 422).json(t); p["code"] != "validation_error" {
		t.Errorf("%v", p)
	}
	a.requireBalance(t, mustParseUUID(t, wallet), max)
}
