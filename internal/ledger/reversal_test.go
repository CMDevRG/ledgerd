package ledger

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"
)

// asTransaction turns an accepted request and its application into the
// Transaction that would be stored.
func asTransaction(id uuid.UUID, req TransactionRequest, res Application) Transaction {
	tx := Transaction{ID: id, Description: req.Description}
	for i, ap := range res.Postings {
		tx.Postings = append(tx.Postings, Posting{
			ID: int64(i + 1), TransactionID: id,
			AccountID: ap.AccountID, Direction: ap.Direction,
			Amount: ap.Amount, Currency: ap.Currency, BalanceAfter: ap.BalanceAfter,
		})
	}
	return tx
}

func TestReverse_MirrorsPostings(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tx := Transaction{ID: id, Postings: []Posting{
		{AccountID: acctA, Direction: Debit, Amount: mustAmount(t, "100"), Currency: "USD"},
		{AccountID: acctB, Direction: Credit, Amount: mustAmount(t, "60"), Currency: "USD"},
		{AccountID: acctC, Direction: Credit, Amount: mustAmount(t, "40"), Currency: "USD"},
	}}

	rev, err := Reverse(tx)
	if err != nil {
		t.Fatal(err)
	}
	if rev.ReversesID != id {
		t.Errorf("ReversesID = %s, want %s", rev.ReversesID, id)
	}
	if !strings.Contains(rev.Request.Description, id.String()) {
		t.Errorf("description should reference the original: %q", rev.Request.Description)
	}
	want := []struct {
		acct uuid.UUID
		dir  Side
		amt  string
	}{{acctA, Credit, "100"}, {acctB, Debit, "60"}, {acctC, Debit, "40"}}
	if len(rev.Request.Postings) != len(want) {
		t.Fatalf("got %d postings, want %d", len(rev.Request.Postings), len(want))
	}
	for i, w := range want {
		g := rev.Request.Postings[i]
		if g.AccountID != w.acct || g.Direction != w.dir || g.Amount.String() != w.amt || g.Currency != "USD" {
			t.Errorf("posting %d = %+v, want %+v", i, g, w)
		}
	}
	if err := rev.Request.Validate(); err != nil {
		t.Errorf("a reversal must itself be a valid, balanced request: %v", err)
	}
}

func TestReverse_Errors(t *testing.T) {
	t.Run("transaction without id", func(t *testing.T) {
		if _, err := Reverse(Transaction{}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("transaction with a single posting is corrupt", func(t *testing.T) {
		tx := Transaction{ID: uuid.New(), Postings: []Posting{
			{AccountID: acctA, Direction: Debit, Amount: mustAmount(t, "1"), Currency: "USD"},
		}}
		_, err := Reverse(tx)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("want a wrapped *ValidationError, got %v", err)
		}
	})
	t.Run("unbalanced stored transaction is refused", func(t *testing.T) {
		tx := Transaction{ID: uuid.New(), Postings: []Posting{
			{AccountID: acctA, Direction: Debit, Amount: mustAmount(t, "5"), Currency: "USD"},
			{AccountID: acctB, Direction: Credit, Amount: mustAmount(t, "4"), Currency: "USD"},
		}}
		if _, err := Reverse(tx); !errors.Is(err, ErrUnbalanced) {
			t.Errorf("want ErrUnbalanced, got %v", err)
		}
	})
}

func TestReverse_UndoingASpentDepositIsRejected(t *testing.T) {
	// Deposit 100 into a wallet, spend it all, then try to reverse the deposit.
	accts := accountMap(
		acct(t, acctA, "USD", Debit, false, "0"),
		acct(t, acctB, "USD", Credit, false, "0"),
	)
	depositReq := simpleRequest(t) // A debit 10000 / B credit 10000
	dep, err := Apply(accts, depositReq)
	if err != nil {
		t.Fatal(err)
	}
	depositTx := asTransaction(uuid.New(), depositReq, dep)
	for id, bal := range dep.NewBalances {
		a := accts[id]
		a.Balance = bal
		accts[id] = a
	}

	// The wallet (B) spends everything: debit B, credit A.
	spend := TransactionRequest{Postings: []PostingInput{
		posting(t, acctB, Debit, "10000", "USD"),
		posting(t, acctA, Credit, "10000", "USD"),
	}}
	sp, err := Apply(accts, spend)
	if err != nil {
		t.Fatal(err)
	}
	for id, bal := range sp.NewBalances {
		a := accts[id]
		a.Balance = bal
		accts[id] = a
	}

	// Reversing the deposit would take 10000 out of an empty wallet.
	rev, err := Reverse(depositTx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(accts, rev.Request); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("reversal must obey the overdraft rule, got %v", err)
	}
}

// Property: for any accepted transaction T applied to a valid state, applying
// Reverse(T) immediately afterwards is always accepted and restores every
// balance exactly (the final balances equal the starting ones, which already
// satisfied the overdraft rule).
func TestProperty_ReversalRestoresBalances(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state := map[uuid.UUID]Account{}
		for _, cur := range testCurrencies {
			for i := 0; i < 3; i++ {
				id := testAccountID(cur, i)
				state[id] = Account{
					ID: id, Currency: cur,
					NormalBalance: rapid.SampledFrom([]Side{Debit, Credit}).Draw(t, "normal"),
					AllowNegative: rapid.Bool().Draw(t, "allowNeg"),
					Balance:       genAmountBig().Draw(t, "opening"),
				}
			}
		}

		req := genBalancedRequest().Draw(t, "request")
		res, err := Apply(state, req)
		if err != nil {
			t.Skip("transaction rejected; nothing to reverse")
		}

		after := make(map[uuid.UUID]Account, len(state))
		for id, a := range state {
			if nb, ok := res.NewBalances[id]; ok {
				a.Balance = nb
			}
			after[id] = a
		}

		rev, err := Reverse(asTransaction(uuid.New(), req, res))
		if err != nil {
			t.Fatalf("Reverse failed: %v", err)
		}
		back, err := Apply(after, rev.Request)
		if err != nil {
			t.Fatalf("reversal of an accepted transaction was rejected: %v", err)
		}
		for id, a := range state {
			final := after[id].Balance
			if nb, ok := back.NewBalances[id]; ok {
				final = nb
			}
			if final.Cmp(a.Balance) != 0 {
				t.Fatalf("account %s: %s after reversal, started at %s", id, final, a.Balance)
			}
		}
	})
}

// Property: reversing twice yields the original postings (involution).
func TestProperty_ReverseIsAnInvolution(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := genBalancedRequest().Draw(t, "request")
		tx := Transaction{ID: uuid.New()}
		for _, p := range req.Postings {
			tx.Postings = append(tx.Postings, Posting{AccountID: p.AccountID, Direction: p.Direction, Amount: p.Amount, Currency: p.Currency})
		}
		first, err := Reverse(tx)
		if err != nil {
			t.Fatal(err)
		}
		tx2 := Transaction{ID: uuid.New()}
		for _, p := range first.Request.Postings {
			tx2.Postings = append(tx2.Postings, Posting{AccountID: p.AccountID, Direction: p.Direction, Amount: p.Amount, Currency: p.Currency})
		}
		second, err := Reverse(tx2)
		if err != nil {
			t.Fatal(err)
		}
		for i, p := range second.Request.Postings {
			o := req.Postings[i]
			if p.AccountID != o.AccountID || p.Direction != o.Direction || p.Amount.Cmp(o.Amount) != 0 || p.Currency != o.Currency {
				t.Fatalf("posting %d changed after two reversals: %+v vs %+v", i, p, o)
			}
		}
	})
}
