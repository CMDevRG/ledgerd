package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/ledger"
)

func TestCreateTransaction_Deposit(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)

	req := transfer(t, cash.ID, wallet.ID, "10000", "USD")
	req.Description = "Deposit 100.00"
	tx := e.mustCreate(t, req)

	if tx.ID == uuid.Nil || tx.Description != "Deposit 100.00" || tx.ReversesID != nil {
		t.Errorf("unexpected transaction: %+v", tx)
	}
	if len(tx.Postings) != 2 {
		t.Fatalf("got %d postings, want 2", len(tx.Postings))
	}
	if tx.Postings[0].ID >= tx.Postings[1].ID {
		t.Error("posting ids must follow request order")
	}
	for i, p := range tx.Postings {
		if p.TransactionID != tx.ID || p.Amount.String() != "10000" || p.BalanceAfter.String() != "10000" || p.Currency != "USD" {
			t.Errorf("posting %d = %+v", i, p)
		}
		if !p.RecordedAt.Equal(tx.CreatedAt) {
			t.Errorf("posting %d recorded at %v, transaction at %v: they must share one timestamp", i, p.RecordedAt, tx.CreatedAt)
		}
	}
	if tx.Postings[0].Direction != ledger.Debit || tx.Postings[0].AccountID != cash.ID {
		t.Errorf("first posting should debit the cash account: %+v", tx.Postings[0])
	}

	e.requireBalance(t, cash.ID, "10000")
	e.requireBalance(t, wallet.ID, "10000")

	a, err := e.svc.GetAccount(context.Background(), cash.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.LastPostingAt == nil || !a.LastPostingAt.Equal(tx.CreatedAt) {
		t.Errorf("last_posting_at = %v, want %v", a.LastPostingAt, tx.CreatedAt)
	}
}

func TestCreateTransaction_TimestampsAreServerSideAndMicrosecondPrecise(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)

	tx := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "1", "USD"))

	want := e.clock.Now().UTC().Truncate(time.Microsecond) // the fake clock has nanoseconds
	if !tx.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want the clock truncated to microseconds (%v)", tx.CreatedAt, want)
	}
}

func TestCreateTransaction_BalanceAfterChain(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)

	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10000", "USD"))     // deposit 100.00
	w := e.mustCreate(t, transfer(t, wallet.ID, cash.ID, "2500", "USD")) // withdraw 25.00

	if got := w.Postings[0].BalanceAfter.String(); got != "7500" { // the wallet, debited
		t.Errorf("wallet balance_after = %s, want 7500", got)
	}
	if got := w.Postings[1].BalanceAfter.String(); got != "7500" { // the cash account, credited
		t.Errorf("cash balance_after = %s, want 7500", got)
	}
	e.requireBalance(t, cash.ID, "7500")
	e.requireBalance(t, wallet.ID, "7500")
}

func TestCreateTransaction_Rejections_PersistNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("unbalanced", func(t *testing.T) {
		e := newEnv(t)
		cash, wallet := e.cashAndWallet(t)
		req := transfer(t, cash.ID, wallet.ID, "100", "USD")
		req.Postings[1].Amount = amt(t, "99")
		if _, err := e.svc.CreateTransaction(ctx, req); !errors.Is(err, ledger.ErrUnbalanced) {
			t.Fatalf("want ErrUnbalanced, got %v", err)
		}
		e.requireNothingPersisted(t)
		e.requireBalance(t, cash.ID, "0")
	})

	t.Run("structurally invalid", func(t *testing.T) {
		e := newEnv(t)
		cash, _ := e.cashAndWallet(t)
		_, err := e.svc.CreateTransaction(ctx, ledger.TransactionRequest{Postings: []ledger.PostingInput{in(t, cash.ID, ledger.Debit, "1", "USD")}})
		var ve *ledger.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("want *ValidationError, got %v", err)
		}
		e.requireNothingPersisted(t)
	})

	t.Run("unknown account", func(t *testing.T) {
		e := newEnv(t)
		cash, _ := e.cashAndWallet(t)
		ghost := uuid.New()
		_, err := e.svc.CreateTransaction(ctx, transfer(t, cash.ID, ghost, "5", "USD"))
		var nf *ledger.AccountNotFoundError
		if !errors.As(err, &nf) || nf.ID != ghost {
			t.Fatalf("want AccountNotFoundError for %s, got %v", ghost, err)
		}
		e.requireNothingPersisted(t)
		e.requireBalance(t, cash.ID, "0")
	})

	t.Run("currency differs from the account's", func(t *testing.T) {
		e := newEnv(t)
		cash, _ := e.cashAndWallet(t)
		eur := e.account(t, "eur-wallet", "EUR", ledger.Liability, ledger.Credit, false)
		// Balanced in USD terms, but the second posting claims USD for an EUR account.
		_, err := e.svc.CreateTransaction(ctx, transfer(t, cash.ID, eur.ID, "5", "USD"))
		var ve *ledger.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("want *ValidationError, got %v", err)
		}
		e.requireNothingPersisted(t)
	})

	t.Run("overdraft on one account", func(t *testing.T) {
		e := newEnv(t)
		// Only the wallet is protected, so it is the only account that can be reported.
		cash := e.account(t, "bank-cash", "USD", ledger.Asset, ledger.Debit, true)
		wallet := e.account(t, "user-wallet", "USD", ledger.Liability, ledger.Credit, false)
		e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "50", "USD"))

		_, err := e.svc.CreateTransaction(ctx, transfer(t, wallet.ID, cash.ID, "80", "USD"))
		var ife *ledger.InsufficientFundsError
		if !errors.Is(err, ledger.ErrInsufficientFunds) || !errors.As(err, &ife) {
			t.Fatalf("want insufficient funds, got %v", err)
		}
		if ife.AccountID != wallet.ID || ife.Shortfall != "30" {
			t.Errorf("unexpected details: %+v", ife)
		}
		e.requireBalance(t, wallet.ID, "50")
		e.requireBalance(t, cash.ID, "50")
		if n := e.count(t, "transactions"); n != 1 {
			t.Errorf("%d transactions, want only the funding one", n)
		}
	})

	t.Run("overdraft on one of several accounts rejects them all", func(t *testing.T) {
		e := newEnv(t)
		cash, wallet := e.cashAndWallet(t)
		other := e.account(t, "other-wallet", "USD", ledger.Liability, ledger.Credit, false)
		e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "1000", "USD"))
		e.mustCreate(t, transfer(t, cash.ID, other.ID, "10", "USD"))

		// 'wallet' can pay 100 but 'other' (10) cannot: the whole transaction fails.
		req := ledger.TransactionRequest{Postings: []ledger.PostingInput{
			in(t, wallet.ID, ledger.Debit, "100", "USD"),
			in(t, other.ID, ledger.Debit, "100", "USD"),
			in(t, cash.ID, ledger.Credit, "200", "USD"),
		}}
		if _, err := e.svc.CreateTransaction(ctx, req); !errors.Is(err, ledger.ErrInsufficientFunds) {
			t.Fatalf("want insufficient funds, got %v", err)
		}
		e.requireBalance(t, wallet.ID, "1000")
		e.requireBalance(t, other.ID, "10")
		e.requireBalance(t, cash.ID, "1010")
	})
}

func TestCreateTransaction_AllowNegativeAccountsMayGoBelowZero(t *testing.T) {
	e := newEnv(t)
	cash := e.account(t, "clearing", "USD", ledger.Asset, ledger.Debit, true)
	wallet := e.account(t, "credit-line", "USD", ledger.Liability, ledger.Credit, true)

	e.mustCreate(t, transfer(t, wallet.ID, cash.ID, "40", "USD"))
	e.requireBalance(t, wallet.ID, "-40")
	e.requireBalance(t, cash.ID, "-40")
}

func TestCreateTransaction_SeveralPostingsOnTheSameAccount(t *testing.T) {
	e := newEnv(t)
	cash := e.account(t, "cash", "USD", ledger.Asset, ledger.Debit, false) // must not go negative
	other := e.account(t, "other", "USD", ledger.Asset, ledger.Debit, true)

	// cash: credit 10 then debit 10. It dips to -10 in the middle and ends at 0.
	tx := e.mustCreate(t, ledger.TransactionRequest{Postings: []ledger.PostingInput{
		in(t, cash.ID, ledger.Credit, "10", "USD"),
		in(t, other.ID, ledger.Debit, "10", "USD"),
		in(t, cash.ID, ledger.Debit, "10", "USD"),
		in(t, other.ID, ledger.Credit, "10", "USD"),
	}})

	want := []string{"-10", "10", "0", "0"}
	for i, p := range tx.Postings {
		if p.BalanceAfter.String() != want[i] {
			t.Errorf("posting %d balance_after = %s, want %s", i, p.BalanceAfter, want[i])
		}
	}
	for i := 1; i < len(tx.Postings); i++ {
		if tx.Postings[i-1].ID >= tx.Postings[i].ID {
			t.Error("posting ids must follow request order")
		}
	}
	e.requireBalance(t, cash.ID, "0")
	e.requireBalance(t, other.ID, "0")
}

func TestCreateTransaction_AmountsBeyondInt64AreExact(t *testing.T) {
	e := newEnv(t)
	cash := e.account(t, "treasury", "ETH", ledger.Asset, ledger.Debit, false)
	wallet := e.account(t, "user-eth", "ETH", ledger.Liability, ledger.Credit, false)

	const big1 = "9223372036854775809" // 2^63 + 1
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, big1, "ETH"))
	tx := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, big1, "ETH"))

	const want = "18446744073709551618" // 2 * (2^63 + 1)
	e.requireBalance(t, wallet.ID, want)
	if got := tx.Postings[0].BalanceAfter.String(); got != want {
		t.Errorf("balance_after = %s, want %s", got, want)
	}

	const max = "1000000000000000000000000000000" // 10^30, the largest allowed posting
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, max, "ETH"))
	e.requireBalance(t, wallet.ID, "1000000000018446744073709551618")
}

func TestCreateTransaction_RecordedAtNeverGoesBackwardsForAnAccount(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)

	first := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10", "USD"))

	// The clock jumps back an hour (NTP step, or a server with a skewed clock).
	e.clock.Advance(-time.Hour)
	second := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10", "USD"))

	if second.CreatedAt.Before(first.CreatedAt) {
		t.Errorf("recorded_at went backwards: %v after %v", second.CreatedAt, first.CreatedAt)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("second transaction should reuse the account's last timestamp %v, got %v", first.CreatedAt, second.CreatedAt)
	}

	// Once the clock moves past it, real time is used again.
	e.clock.Advance(2 * time.Hour)
	third := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10", "USD"))
	if !third.CreatedAt.After(first.CreatedAt) {
		t.Errorf("third transaction at %v should be later than %v", third.CreatedAt, first.CreatedAt)
	}
}

// If the database refuses the write half-way (here: a balance that would not
// fit in NUMERIC(38,0)), nothing from the transaction may remain, including
// the rows that were already inserted before the failure.
func TestCreateTransaction_DatabaseFailureMidwayRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	// Put the cash account 5 units below the 38-digit limit.
	if _, err := e.db.Super.Exec(ctx,
		`UPDATE accounts SET balance = 99999999999999999999999999999999999995 WHERE id = $1`, cash.ID); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.CreateTransaction(ctx, transfer(t, cash.ID, wallet.ID, "10", "USD"))
	if err == nil {
		t.Fatal("expected the database to refuse a balance beyond 38 digits")
	}
	requirePgError(t, err, "22003", "numeric field overflow")

	e.requireNothingPersisted(t)
	e.requireBalance(t, cash.ID, "99999999999999999999999999999999999995")
	e.requireBalance(t, wallet.ID, "0")
}
