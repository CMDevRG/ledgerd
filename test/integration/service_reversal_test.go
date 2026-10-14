package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
)

func TestReverse_OffsetsTheOriginal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	original := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10000", "USD"))
	rev, err := e.reverse(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}

	if rev.ReversesID == nil || *rev.ReversesID != original.ID {
		t.Errorf("ReversesID = %v, want %s", rev.ReversesID, original.ID)
	}
	if rev.ID == original.ID {
		t.Error("a reversal is a new transaction")
	}
	if len(rev.Postings) != len(original.Postings) {
		t.Fatalf("got %d postings, want %d", len(rev.Postings), len(original.Postings))
	}
	for i, p := range rev.Postings {
		o := original.Postings[i]
		if p.AccountID != o.AccountID || p.Amount.Cmp(o.Amount) != 0 || p.Currency != o.Currency || p.Direction != o.Direction.Opposite() {
			t.Errorf("posting %d = %+v, want the mirror of %+v", i, p, o)
		}
	}

	e.requireBalance(t, cash.ID, "0")
	e.requireBalance(t, wallet.ID, "0")

	// The original is untouched: still there, with its own two postings.
	again, err := e.st.GetTransaction(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Postings) != 2 || again.ReversesID != nil {
		t.Errorf("original was altered: %+v", again)
	}
	if n := e.count(t, "transactions"); n != 2 {
		t.Errorf("%d transactions, want 2 (original + reversal)", n)
	}
}

func TestReverse_CanOnlyHappenOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	original := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))

	if _, err := e.reverse(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.reverse(ctx, original.ID)
	if !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("want ErrAlreadyReversed, got %v", err)
	}
	if n := e.count(t, "transactions"); n != 2 {
		t.Errorf("%d transactions, want 2", n)
	}
	e.requireBalance(t, wallet.ID, "0")
}

func TestReverse_UnknownTransaction(t *testing.T) {
	e := newEnv(t)
	missing := uuid.New()
	_, err := e.reverse(context.Background(), missing)
	var nf *ledger.TransactionNotFoundError
	if !errors.Is(err, ledger.ErrNotFound) || !errors.As(err, &nf) || nf.ID != missing {
		t.Fatalf("want TransactionNotFoundError for %s, got %v", missing, err)
	}
}

func TestReverse_ObeysTheOverdraftRule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	deposit := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10000", "USD"))
	e.mustCreate(t, transfer(t, wallet.ID, cash.ID, "10000", "USD")) // the user spends it all

	_, err := e.reverse(ctx, deposit.ID)
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("undoing a spent deposit must be rejected, got %v", err)
	}
	if n := e.count(t, "transactions"); n != 2 {
		t.Errorf("%d transactions, want 2 (the rejected reversal leaves nothing)", n)
	}

	// The failed attempt did not "use up" the single reversal: once the money is
	// back, the deposit can be reversed.
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10000", "USD"))
	if _, err := e.reverse(ctx, deposit.ID); err != nil {
		t.Fatalf("reversal should now be accepted: %v", err)
	}
	e.requireBalance(t, wallet.ID, "0")
	e.requireBalance(t, cash.ID, "0")
}

func TestReverse_AReversalCanItselfBeReversed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	original := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "500", "USD"))
	rev, err := e.reverse(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.reverse(ctx, rev.ID)
	if err != nil {
		t.Fatalf("reversing a reversal re-applies the original effect: %v", err)
	}
	if again.ReversesID == nil || *again.ReversesID != rev.ID {
		t.Errorf("ReversesID = %v, want %s", again.ReversesID, rev.ID)
	}
	e.requireBalance(t, wallet.ID, "500")
	e.requireBalance(t, cash.ID, "500")
}

func TestReverse_MultiPostingMultiCurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	usdCash := e.account(t, "usd-cash", "USD", ledger.Asset, ledger.Debit, true)
	usdWallet := e.account(t, "usd-wallet", "USD", ledger.Liability, ledger.Credit, true)
	usdFee := e.account(t, "usd-fees", "USD", ledger.Income, ledger.Credit, true)
	ethCash := e.account(t, "eth-cash", "ETH", ledger.Asset, ledger.Debit, true)
	ethWallet := e.account(t, "eth-wallet", "ETH", ledger.Liability, ledger.Credit, true)

	original := e.mustCreate(t, ledger.TransactionRequest{Postings: []ledger.PostingInput{
		in(t, usdCash.ID, ledger.Debit, "1000", "USD"),
		in(t, usdWallet.ID, ledger.Credit, "990", "USD"),
		in(t, usdFee.ID, ledger.Credit, "10", "USD"),
		in(t, ethCash.ID, ledger.Debit, "9223372036854775809", "ETH"),
		in(t, ethWallet.ID, ledger.Credit, "9223372036854775809", "ETH"),
	}})
	if _, err := e.reverse(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []ledger.Account{usdCash, usdWallet, usdFee, ethCash, ethWallet} {
		e.requireBalance(t, a.ID, "0")
	}
}

// Many callers race to reverse the same transaction. Exactly one wins and all
// the others are told "already reversed", even though by the time they get the
// account locks the balances are back to zero and a second reversal would also
// have overdrawn the wallet. This pins down the ORDER of the checks: "already
// reversed" must be reported before the overdraft check.
func TestReverse_ConcurrentAttemptsExactlyOneWins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	original := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "10000", "USD"))

	const callers = 20
	results := make([]error, callers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, results[i] = e.reverse(ctx, original.ID)
		}()
	}
	start.Done()
	done.Wait()

	wins, already, other := 0, 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ledger.ErrAlreadyReversed):
			already++
		default:
			other++
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 || already != callers-1 || other != 0 {
		t.Errorf("wins=%d already_reversed=%d other=%d, want 1 / %d / 0", wins, already, other, callers-1)
	}
	if n := e.count(t, "transactions"); n != 2 {
		t.Errorf("%d transactions, want 2 (original + exactly one reversal)", n)
	}
	e.requireBalance(t, wallet.ID, "0")
	e.requireBalance(t, cash.ID, "0")
}
