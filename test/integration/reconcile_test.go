package integration

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/reconcile"
	"github.com/CMDevRG/ledgerd/internal/store"
)

func (e *env) reconcile(t *testing.T, deep bool) ledger.ReconciliationReport {
	t.Helper()
	r, err := reconcile.Run(context.Background(), e.st, reconcile.Options{Deep: deep, Now: e.clock.Now})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return r
}

// withAppendOnlyDisabled lets a test tamper with history the way only a
// superuser could, then restores the protection.
func (e *env) withAppendOnlyDisabled(t *testing.T, tamper func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.Super.Exec(ctx, `ALTER TABLE postings DISABLE TRIGGER postings_append_only`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := e.db.Super.Exec(ctx, `ALTER TABLE postings ENABLE TRIGGER postings_append_only`); err != nil {
			t.Fatal(err)
		}
	}()
	tamper()
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.db.Super.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// busyLedger creates a ledger with deposits, withdrawals, a reversal, several
// currencies and an account that may go negative.
func (e *env) busyLedger(t *testing.T) (accounts []ledger.Account) {
	t.Helper()
	cash, wallet := e.cashAndWallet(t)
	loan := e.account(t, "credit-line", "USD", ledger.Liability, ledger.Credit, true)
	ethTreasury := e.account(t, "eth-treasury", "ETH", ledger.Asset, ledger.Debit, false)
	ethWallet := e.account(t, "eth-wallet", "ETH", ledger.Liability, ledger.Credit, false)

	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "20000", "USD"))
	spend := e.mustCreate(t, transfer(t, wallet.ID, cash.ID, "2500", "USD"))
	e.mustCreate(t, transfer(t, wallet.ID, loan.ID, "700", "USD"))
	e.mustCreate(t, transfer(t, loan.ID, cash.ID, "9000", "USD")) // the credit line goes negative (allowed for it)
	if _, err := e.reverse(context.Background(), spend.ID); err != nil {
		t.Fatal(err)
	}
	e.mustCreate(t, transfer(t, ethTreasury.ID, ethWallet.ID, "9223372036854775809", "ETH"))
	e.mustCreate(t, transfer(t, ethWallet.ID, ethTreasury.ID, "1000000000000000000", "ETH"))
	return []ledger.Account{cash, wallet, loan, ethTreasury, ethWallet}
}

func TestReconcile_CleanLedger(t *testing.T) {
	e := newEnv(t)
	accounts := e.busyLedger(t)

	for _, deep := range []bool{false, true} {
		r := e.reconcile(t, deep)
		if !r.OK() {
			t.Fatalf("deep=%v: a ledger built only through the service must be consistent: %+v", deep, r)
		}
		if r.AccountsChecked != int64(len(accounts)) || r.Deep != deep || len(r.Mismatches) != 0 || len(r.ChainBreaks) != 0 {
			t.Errorf("deep=%v: unexpected report: %+v", deep, r)
		}
		if !r.StartedAt.Equal(e.clock.Now().UTC()) {
			t.Errorf("StartedAt = %v, want the injected clock %v", r.StartedAt, e.clock.Now())
		}
	}

	r := e.reconcile(t, false)
	if len(r.Currencies) != 2 || r.Currencies[0].Currency != "ETH" || r.Currencies[1].Currency != "USD" {
		t.Fatalf("want ETH then USD totals, got %+v", r.Currencies)
	}
	for _, c := range r.Currencies {
		if !c.Balanced() || c.Debits.Sign() <= 0 {
			t.Errorf("%s: debits=%s credits=%s", c.Currency, c.Debits, c.Credits)
		}
	}
}

func TestReconcile_EmptyLedgers(t *testing.T) {
	e := newEnv(t)
	if r := e.reconcile(t, true); !r.OK() || r.AccountsChecked != 0 || len(r.Currencies) != 0 {
		t.Errorf("no accounts: %+v", r)
	}
	e.cashAndWallet(t)
	if r := e.reconcile(t, true); !r.OK() || r.AccountsChecked != 2 || len(r.Currencies) != 0 {
		t.Errorf("accounts without postings: %+v", r)
	}
}

func TestReconcile_DetectsACorruptedBalance(t *testing.T) {
	e := newEnv(t)
	accounts := e.busyLedger(t)
	victim := accounts[1] // user wallet
	before := e.balance(t, victim.ID)

	e.exec(t, `UPDATE accounts SET balance = balance + 3 WHERE id = $1`, victim.ID)
	corrupted := e.balance(t, victim.ID)

	r := e.reconcile(t, true)
	if r.OK() || len(r.Mismatches) != 1 {
		t.Fatalf("want exactly one mismatch, got %+v", r)
	}
	m := r.Mismatches[0]
	if m.AccountID != victim.ID || m.Code != victim.Code || m.Currency != "USD" ||
		m.Stored.Cmp(corrupted) != 0 || m.Expected.Cmp(before) != 0 {
		t.Errorf("mismatch = %+v; want stored %s, expected %s", m, corrupted, before)
	}
	if len(r.ChainBreaks) != 0 {
		t.Errorf("the postings are intact, no chain breaks expected: %+v", r.ChainBreaks)
	}
	for _, c := range r.Currencies {
		if !c.Balanced() {
			t.Errorf("%s totals come from postings and must still balance", c.Currency)
		}
	}

	// Reconciliation only reports. It never repairs.
	if again := e.reconcile(t, false); len(again.Mismatches) != 1 {
		t.Errorf("a second run must find the same problem: %+v", again)
	}
	if e.balance(t, victim.ID).Cmp(corrupted) != 0 {
		t.Error("reconciliation modified a balance")
	}
}

func TestReconcile_ReportsEveryMismatchInIdOrder(t *testing.T) {
	e := newEnv(t)
	accounts := e.busyLedger(t)
	for _, a := range accounts {
		e.exec(t, `UPDATE accounts SET balance = balance + 1 WHERE id = $1`, a.ID)
	}
	r := e.reconcile(t, false)
	if len(r.Mismatches) != len(accounts) {
		t.Fatalf("got %d mismatches, want %d", len(r.Mismatches), len(accounts))
	}
	for i := 1; i < len(r.Mismatches); i++ {
		a, b := r.Mismatches[i-1].AccountID, r.Mismatches[i].AccountID
		if a.String() >= b.String() {
			t.Errorf("mismatches not ordered by id: %s then %s", a, b)
		}
	}
}

func TestReconcile_DetectsTamperedPostings(t *testing.T) {
	e := newEnv(t)
	accounts := e.busyLedger(t)
	wallet := accounts[1]

	var target int64
	if err := e.db.Super.QueryRow(context.Background(),
		`SELECT id FROM postings WHERE account_id = $1 AND direction = 'CREDIT' ORDER BY id LIMIT 1`, wallet.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	e.withAppendOnlyDisabled(t, func() {
		e.exec(t, `UPDATE postings SET amount = amount + 5 WHERE id = $1`, target)
	})

	r := e.reconcile(t, true)
	if r.OK() {
		t.Fatal("tampering with a posting must be detected")
	}
	// 1. The wallet's balance no longer equals the sum of its postings.
	if len(r.Mismatches) != 1 || r.Mismatches[0].AccountID != wallet.ID {
		t.Errorf("want one mismatch on the wallet, got %+v", r.Mismatches)
	}
	// 2. USD credits now exceed debits by exactly 5; ETH is unaffected.
	for _, c := range r.Currencies {
		switch {
		case c.Currency == "USD" && (c.Balanced() || c.Credits.Cmp(c.Debits) <= 0):
			t.Errorf("USD should show credits > debits: %+v", c)
		case c.Currency == "ETH" && !c.Balanced():
			t.Errorf("ETH should be untouched: %+v", c)
		}
	}
	// 3. The deep check pinpoints the altered posting.
	if len(r.ChainBreaks) != 1 || r.ChainBreaks[0].PostingID != target {
		t.Errorf("want one chain break at posting %d, got %+v", target, r.ChainBreaks)
	}
	// The shallow check does not look at chains.
	if shallow := e.reconcile(t, false); len(shallow.ChainBreaks) != 0 || shallow.OK() {
		t.Errorf("shallow run: want no chain data but still not OK: %+v", shallow)
	}
}

func TestReconcile_DeepCheckFindsABrokenBalanceChain(t *testing.T) {
	e := newEnv(t)
	accounts := e.busyLedger(t)
	cash := accounts[0]

	ids := e.postingIDs(t, cash.ID)
	if len(ids) < 3 {
		t.Fatalf("scenario needs at least 3 postings on the cash account, have %d", len(ids))
	}
	middle := ids[1]
	e.withAppendOnlyDisabled(t, func() {
		e.exec(t, `UPDATE postings SET balance_after = balance_after + 7 WHERE id = $1`, middle)
	})

	shallow := e.reconcile(t, false)
	if !shallow.OK() {
		t.Errorf("balances and totals are intact, so the shallow check passes: %+v", shallow)
	}

	deep := e.reconcile(t, true)
	if deep.OK() {
		t.Fatal("the deep check must detect the altered balance_after")
	}
	if len(deep.ChainBreaks) != 2 || deep.ChainBreaks[0].PostingID != middle || deep.ChainBreaks[1].PostingID != ids[2] {
		t.Errorf("want breaks at the altered posting %d and the next one %d, got %+v", middle, ids[2], deep.ChainBreaks)
	}
	if b := deep.ChainBreaks[0]; b.Recorded.Cmp(b.Expected) != 1 || new(big.Int).Sub(b.Recorded, b.Expected).Int64() != 7 {
		t.Errorf("the break should show recorded = expected + 7: %+v", b)
	}
}

func (e *env) postingIDs(t *testing.T, account uuid.UUID) []int64 {
	t.Helper()
	rows, err := e.db.Super.Query(context.Background(), `SELECT id FROM postings WHERE account_id = $1 ORDER BY id`, account)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// The reconciliation runs in one REPEATABLE READ, READ ONLY transaction:
// whatever commits while it runs is invisible to it.
func TestReconcile_UsesAConsistentReadOnlySnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))

	err := e.st.ReadSnapshot(ctx, func(ctx context.Context, snap *store.Snapshot) error {
		isolation, readOnly, err := snap.Settings(ctx) // also takes the snapshot
		if err != nil {
			return err
		}
		if isolation != "repeatable read" || !readOnly {
			t.Errorf("isolation=%q readOnly=%v, want repeatable read and read-only", isolation, readOnly)
		}

		before, err := snap.CountAccounts(ctx)
		if err != nil {
			return err
		}
		totalsBefore, err := snap.CurrencyTotals(ctx)
		if err != nil {
			return err
		}

		// Meanwhile, other sessions commit a new account and a new transaction.
		e.account(t, "late-comer", "USD", ledger.Asset, ledger.Debit, true)
		e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "999", "USD"))

		after, err := snap.CountAccounts(ctx)
		if err != nil {
			return err
		}
		totalsAfter, err := snap.CurrencyTotals(ctx)
		if err != nil {
			return err
		}
		if before != after {
			t.Errorf("the snapshot saw the new account: %d then %d", before, after)
		}
		if totalsBefore[0].Debits.Cmp(totalsAfter[0].Debits) != 0 {
			t.Errorf("the snapshot saw the new transaction: %s then %s", totalsBefore[0].Debits, totalsAfter[0].Debits)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if r := e.reconcile(t, true); r.AccountsChecked != 3 || r.Currencies[0].Debits.String() != "1099" {
		t.Errorf("a fresh run must see everything that was committed: %+v", r)
	}
}

// Reconciliation takes no row locks, so it neither waits for nor blocks writers.
func TestReconcile_DoesNotWaitForWriters(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cash, wallet := e.cashAndWallet(t)
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))

	// A writer holds the wallet's row lock and has an uncommitted change.
	writer, err := e.db.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Rollback(context.Background()) })
	if _, err := writer.Exec(ctx, `UPDATE accounts SET balance = balance + 1 WHERE id = $1`, wallet.ID); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	r, err := reconcile.Run(ctx, e.st, reconcile.Options{Deep: true})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("reconcile took %v while a writer held a lock: it must not wait", time.Since(start))
	}
	if !r.OK() {
		t.Errorf("the uncommitted change is invisible, so the report must be clean: %+v", r)
	}
}

// Transactions keep committing while reconciliations run. Every report must be
// clean: a run sees one consistent moment, never a half-applied transaction.
func TestReconcile_WhileTransactionsAreCommitting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	const writers = 6
	var treasuries, wallets []ledger.Account
	for i := 0; i < writers; i++ {
		treasuries = append(treasuries, e.account(t, fmt.Sprintf("treasury-%d", i), "USD", ledger.Asset, ledger.Debit, true))
	}
	for i := 0; i < 4; i++ {
		wallets = append(wallets, e.account(t, fmt.Sprintf("wallet-%d", i), "USD", ledger.Liability, ledger.Credit, true))
	}

	var stop atomic.Bool
	var committed atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				target := wallets[(w+i)%len(wallets)]
				if _, err := e.create(ctx, transfer(t, treasuries[w].ID, target.ID, "3", "USD")); err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
				committed.Add(1)
			}
		}()
	}

	// Keep reconciling until a meaningful number of transfers has committed
	// (and at least 20 runs), so the runs genuinely overlap with writes.
	runs := 0
	deadline := time.Now().Add(30 * time.Second)
	for (runs < 20 || committed.Load() < 150) && time.Now().Before(deadline) {
		if r := e.reconcile(t, true); !r.OK() {
			t.Errorf("run %d reported a false problem: mismatches=%d breaks=%d currencies=%+v", runs, len(r.Mismatches), len(r.ChainBreaks), r.Currencies)
			break
		}
		runs++
	}
	stop.Store(true)
	wg.Wait()

	if committed.Load() < 100 {
		t.Fatalf("only %d transfers committed while reconciling: the scenario did not exercise concurrency", committed.Load())
	}
	if final := e.reconcile(t, true); !final.OK() {
		t.Fatalf("final state inconsistent: %+v", final)
	}
	total := new(big.Int)
	for _, a := range treasuries {
		total.Add(total, e.balance(t, a.ID))
	}
	if want := fmt.Sprint(3 * committed.Load()); total.String() != want {
		t.Errorf("treasuries hold %s, want 3 × %d committed transfers = %s", total, committed.Load(), want)
	}
	t.Logf("%d transfers committed while %d reconciliations ran", committed.Load(), runs)
}
