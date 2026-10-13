package ledger

import (
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"
)

func bigStr(tb failer, s string) *big.Int {
	tb.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		tb.Fatalf("bad integer %q", s)
	}
	return v
}

func acct(tb failer, id uuid.UUID, cur Currency, normal Side, allowNegative bool, balance string) Account {
	tb.Helper()
	return Account{ID: id, Code: id.String(), Currency: cur, NormalBalance: normal, AllowNegative: allowNegative, Balance: bigStr(tb, balance)}
}

func accountMap(accts ...Account) map[uuid.UUID]Account {
	m := make(map[uuid.UUID]Account, len(accts))
	for _, a := range accts {
		m[a.ID] = a
	}
	return m
}

func TestApply_DepositIncreasesBothAccounts(t *testing.T) {
	// A debit-normal asset (bank cash) and a credit-normal liability (user wallet).
	accts := accountMap(
		acct(t, acctA, "USD", Debit, false, "0"),
		acct(t, acctB, "USD", Credit, false, "0"),
	)
	res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
		posting(t, acctA, Debit, "10000", "USD"),
		posting(t, acctB, Credit, "10000", "USD"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.NewBalances[acctA].String(); got != "10000" {
		t.Errorf("asset balance = %s, want 10000", got)
	}
	if got := res.NewBalances[acctB].String(); got != "10000" {
		t.Errorf("liability balance = %s, want 10000", got)
	}
	if res.Postings[0].BalanceAfter.String() != "10000" || res.Postings[1].BalanceAfter.String() != "10000" {
		t.Errorf("unexpected balance_after values: %v", res.Postings)
	}
}

func TestApply_WithdrawalMovesBalancesTheOtherWay(t *testing.T) {
	accts := accountMap(
		acct(t, acctA, "USD", Debit, false, "10000"),
		acct(t, acctB, "USD", Credit, false, "10000"),
	)
	res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
		posting(t, acctB, Debit, "2500", "USD"),  // wallet debited: liability decreases
		posting(t, acctA, Credit, "2500", "USD"), // cash credited: asset decreases
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewBalances[acctA].String() != "7500" || res.NewBalances[acctB].String() != "7500" {
		t.Errorf("balances = %v", res.NewBalances)
	}
}

func TestApply_Overdraft(t *testing.T) {
	t.Run("rejected with the exact shortfall", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, false, "1000"),
			acct(t, acctB, "USD", Credit, false, "50"),
		)
		_, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
			posting(t, acctB, Debit, "80", "USD"),
			posting(t, acctA, Credit, "80", "USD"),
		}})
		var ife *InsufficientFundsError
		if !errors.Is(err, ErrInsufficientFunds) || !errors.As(err, &ife) {
			t.Fatalf("want insufficient funds, got %v", err)
		}
		if ife.AccountID != acctB || ife.Balance != "-30" || ife.Shortfall != "30" {
			t.Errorf("unexpected details: %+v", ife)
		}
	})
	t.Run("exactly zero is allowed", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, false, "1000"),
			acct(t, acctB, "USD", Credit, false, "50"),
		)
		res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
			posting(t, acctB, Debit, "50", "USD"),
			posting(t, acctA, Credit, "50", "USD"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		if res.NewBalances[acctB].Sign() != 0 {
			t.Errorf("want 0, got %s", res.NewBalances[acctB])
		}
	})
	t.Run("one more unit overdraws", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, false, "1000"),
			acct(t, acctB, "USD", Credit, false, "50"),
		)
		_, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
			posting(t, acctB, Debit, "51", "USD"),
			posting(t, acctA, Credit, "51", "USD"),
		}})
		if !errors.Is(err, ErrInsufficientFunds) {
			t.Fatalf("want insufficient funds, got %v", err)
		}
	})
	t.Run("allow_negative accounts may go below zero", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, true, "0"),
			acct(t, acctB, "USD", Credit, true, "0"),
		)
		res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
			posting(t, acctB, Debit, "40", "USD"),
			posting(t, acctA, Credit, "40", "USD"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		if res.NewBalances[acctA].String() != "-40" || res.NewBalances[acctB].String() != "-40" {
			t.Errorf("balances = %v", res.NewBalances)
		}
	})
	t.Run("one overdrawn account rejects the whole transaction", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, false, "1000"),
			acct(t, acctB, "USD", Credit, false, "10"),
			acct(t, acctC, "USD", Credit, false, "1000"),
		)
		res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
			posting(t, acctC, Debit, "100", "USD"), // fine on its own
			posting(t, acctB, Debit, "100", "USD"), // overdraws B
			posting(t, acctA, Credit, "200", "USD"),
		}})
		if !errors.Is(err, ErrInsufficientFunds) {
			t.Fatalf("want insufficient funds, got %v", err)
		}
		if res.NewBalances != nil || res.Postings != nil {
			t.Error("a rejected transaction must return no partial result")
		}
	})
	t.Run("several overdrawn accounts: lowest id is reported", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, true, "1000"),
			acct(t, acctB, "USD", Credit, false, "0"),
			acct(t, acctC, "USD", Credit, false, "0"),
		)
		req := TransactionRequest{Postings: []PostingInput{
			posting(t, acctC, Debit, "1", "USD"),
			posting(t, acctB, Debit, "1", "USD"),
			posting(t, acctA, Credit, "2", "USD"),
		}}
		for i := 0; i < 50; i++ { // map iteration order is random in Go
			var ife *InsufficientFundsError
			if _, err := Apply(accts, req); !errors.As(err, &ife) || ife.AccountID != acctB {
				t.Fatalf("iteration %d: want account %s, got %v", i, acctB, err)
			}
		}
	})
}

func TestApply_SeveralPostingsOnOneAccount(t *testing.T) {
	// A non-negative, debit-normal account at 0: credit 10 then debit 10.
	// It dips to -10 in the middle and ends at 0. Only the FINAL balance counts.
	accts := accountMap(
		acct(t, acctA, "USD", Debit, false, "0"),
		acct(t, acctB, "USD", Credit, true, "0"),
	)
	res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
		posting(t, acctA, Credit, "10", "USD"),
		posting(t, acctB, Debit, "10", "USD"),
		posting(t, acctA, Debit, "10", "USD"),
		posting(t, acctB, Credit, "10", "USD"),
	}})
	if err != nil {
		t.Fatalf("an intermediate dip must not be an overdraft: %v", err)
	}
	want := []string{"-10", "-10", "0", "0"}
	for i, p := range res.Postings {
		if p.BalanceAfter.String() != want[i] {
			t.Errorf("posting %d balance_after = %s, want %s", i, p.BalanceAfter, want[i])
		}
	}
	if res.NewBalances[acctA].Sign() != 0 {
		t.Errorf("final balance = %s, want 0", res.NewBalances[acctA])
	}

	// If the final balance is negative the transaction is rejected even though
	// the last posting was a debit.
	_, err = Apply(accts, TransactionRequest{Postings: []PostingInput{
		posting(t, acctA, Credit, "10", "USD"),
		posting(t, acctB, Debit, "10", "USD"),
		posting(t, acctA, Debit, "4", "USD"),
		posting(t, acctB, Credit, "4", "USD"),
	}})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("final balance -6 must be rejected, got %v", err)
	}
}

func TestApply_Errors(t *testing.T) {
	t.Run("unknown account", func(t *testing.T) {
		accts := accountMap(acct(t, acctA, "USD", Debit, true, "0"))
		_, err := Apply(accts, simpleRequest(t)) // references acctB, absent
		var nf *AccountNotFoundError
		if !errors.Is(err, ErrNotFound) || !errors.As(err, &nf) || nf.ID != acctB {
			t.Fatalf("want not found for %s, got %v", acctB, err)
		}
	})
	t.Run("currency differs from the account's", func(t *testing.T) {
		accts := accountMap(
			acct(t, acctA, "USD", Debit, true, "0"),
			acct(t, acctB, "EUR", Credit, true, "0"), // request says USD
		)
		_, err := Apply(accts, simpleRequest(t))
		assertViolationOn(t, err, "postings[1].currency")
	})
	t.Run("invalid request is rejected before touching accounts", func(t *testing.T) {
		_, err := Apply(accountMap(), TransactionRequest{})
		assertViolationOn(t, err, "postings")
	})
	t.Run("unbalanced request is rejected", func(t *testing.T) {
		accts := accountMap(acct(t, acctA, "USD", Debit, true, "0"), acct(t, acctB, "USD", Credit, true, "0"))
		r := simpleRequest(t)
		r.Postings[1].Amount = mustAmount(t, "1")
		if _, err := Apply(accts, r); !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("want unbalanced, got %v", err)
		}
	})
}

func TestApply_DoesNotMutateInputs(t *testing.T) {
	accts := accountMap(
		acct(t, acctA, "USD", Debit, true, "100"),
		acct(t, acctB, "USD", Credit, true, "100"),
	)
	if _, err := Apply(accts, simpleRequest(t)); err != nil {
		t.Fatal(err)
	}
	if accts[acctA].Balance.String() != "100" || accts[acctB].Balance.String() != "100" {
		t.Error("Apply mutated the balances of its input accounts")
	}
}

func TestApply_OnlyTouchedAccountsAreReturned(t *testing.T) {
	accts := accountMap(
		acct(t, acctA, "USD", Debit, true, "0"),
		acct(t, acctB, "USD", Credit, true, "0"),
		acct(t, acctC, "USD", Credit, true, "0"),
	)
	res, err := Apply(accts, simpleRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NewBalances) != 2 {
		t.Errorf("want 2 touched accounts, got %d", len(res.NewBalances))
	}
	if _, ok := res.NewBalances[acctC]; ok {
		t.Error("untouched account must not appear in NewBalances")
	}
}

func TestApply_ArithmeticBeyondInt64(t *testing.T) {
	// 2^63 - 1 plus 2^63 - 1 plus 2 overflows int64 many times over.
	accts := accountMap(
		acct(t, acctA, "ETH", Debit, true, "9223372036854775807"),
		acct(t, acctB, "ETH", Credit, true, "9223372036854775807"),
	)
	res, err := Apply(accts, TransactionRequest{Postings: []PostingInput{
		posting(t, acctA, Debit, "9223372036854775809", "ETH"),
		posting(t, acctB, Credit, "9223372036854775809", "ETH"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	const want = "18446744073709551616" // 2^64
	if res.NewBalances[acctA].String() != want || res.NewBalances[acctB].String() != want {
		t.Errorf("balances = %v, want %s", res.NewBalances, want)
	}
}

// debitView sums, per currency, balance for debit-normal accounts and -balance
// for credit-normal accounts. Because every transaction is balanced, this sum
// is invariant: it only changes if money is created or destroyed.
func debitView(state map[uuid.UUID]Account) map[Currency]*big.Int {
	sums := map[Currency]*big.Int{}
	for _, a := range state {
		if sums[a.Currency] == nil {
			sums[a.Currency] = new(big.Int)
		}
		if a.NormalBalance == Debit {
			sums[a.Currency].Add(sums[a.Currency], a.Balance)
		} else {
			sums[a.Currency].Sub(sums[a.Currency], a.Balance)
		}
	}
	return sums
}

// Property: over a long random stream of balanced transactions applied to
// accounts with random normal sides, overdraft flags and (large) opening
// balances:
//   - the verdict (accept/reject) equals an independent net-delta oracle;
//   - a rejected transaction changes nothing;
//   - after every accepted transaction, no non-negative account is negative,
//     the debit-view sum per currency is unchanged (nothing is created or
//     destroyed), and each account's last balance_after equals its balance.
func TestProperty_ApplyPreservesInvariants(t *testing.T) {
	var accepted, rejected int

	rapid.Check(t, func(t *rapid.T) {
		state := map[uuid.UUID]Account{}
		for _, cur := range testCurrencies {
			for i := 0; i < 3; i++ {
				opening := new(big.Int)
				if rapid.Bool().Draw(t, "funded") {
					opening = genAmountBig().Draw(t, "opening")
				}
				id := testAccountID(cur, i)
				state[id] = Account{
					ID: id, Currency: cur,
					NormalBalance: rapid.SampledFrom([]Side{Debit, Credit}).Draw(t, "normal"),
					AllowNegative: rapid.Bool().Draw(t, "allowNeg"),
					Balance:       opening,
				}
			}
		}

		steps := rapid.IntRange(1, 20).Draw(t, "steps")
		for s := 0; s < steps; s++ {
			req := genBalancedRequest().Draw(t, "request")

			// Oracle: net change per account, then check the overdraft rule.
			expected := map[uuid.UUID]*big.Int{}
			for _, p := range req.Postings {
				if expected[p.AccountID] == nil {
					expected[p.AccountID] = new(big.Int).Set(state[p.AccountID].Balance)
				}
				if p.Direction == state[p.AccountID].NormalBalance {
					expected[p.AccountID].Add(expected[p.AccountID], p.Amount.Big())
				} else {
					expected[p.AccountID].Sub(expected[p.AccountID], p.Amount.Big())
				}
			}
			wantReject := false
			for id, bal := range expected {
				if !state[id].AllowNegative && bal.Sign() < 0 {
					wantReject = true
				}
			}

			before := debitView(state)
			snapshot := map[uuid.UUID]string{}
			for id, a := range state {
				snapshot[id] = a.Balance.String()
			}

			res, err := Apply(state, req)

			for id, a := range state { // purity: Apply never changes its input
				if a.Balance.String() != snapshot[id] {
					t.Fatalf("Apply mutated account %s", id)
				}
			}
			if wantReject {
				rejected++
				if !errors.Is(err, ErrInsufficientFunds) {
					t.Fatalf("oracle says overdraft, Apply returned %v", err)
				}
				continue
			}
			accepted++
			if err != nil {
				t.Fatalf("oracle says acceptable, Apply returned %v", err)
			}

			if len(res.Postings) != len(req.Postings) {
				t.Fatalf("got %d applied postings for %d requested", len(res.Postings), len(req.Postings))
			}
			last := map[uuid.UUID]*big.Int{}
			for _, ap := range res.Postings {
				last[ap.AccountID] = ap.BalanceAfter
			}
			for id, bal := range res.NewBalances {
				if bal.Cmp(expected[id]) != 0 {
					t.Fatalf("account %s: Apply=%s oracle=%s", id, bal, expected[id])
				}
				if last[id].Cmp(bal) != 0 {
					t.Fatalf("account %s: last balance_after %s != final balance %s", id, last[id], bal)
				}
				a := state[id]
				a.Balance = bal
				state[id] = a
			}
			for id, a := range state {
				if !a.AllowNegative && a.Balance.Sign() < 0 {
					t.Fatalf("non-negative account %s ended negative: %s", id, a.Balance)
				}
			}
			after := debitView(state)
			for cur, v := range before {
				if v.Cmp(after[cur]) != 0 {
					t.Fatalf("%s: debit-view sum changed from %s to %s (money created or destroyed)", cur, v, after[cur])
				}
			}
		}
	})

	if accepted == 0 || rejected == 0 {
		t.Fatalf("generator is not exercising both outcomes: accepted=%d rejected=%d", accepted, rejected)
	}
}
