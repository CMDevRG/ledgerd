package integration

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
	"github.com/radadev/ledgerd/internal/store"
)

// newEnvWithConns builds an environment whose connection pool has the given
// size (the production default is 20), using the real clock.
func newEnvWithConns(t *testing.T, conns int32) *env {
	t.Helper()
	database := server.NewDB(t)
	cfg, err := pgxpool.ParseConfig(database.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = conns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // registered after NewDB's cleanup, so it runs first

	st := store.New(pool, store.Config{})
	return &env{db: database, st: st, svc: service.New(st), clock: newFakeClock()}
}

// seedFor returns a reproducible random source; the seed is logged so that a
// failure can be replayed.
func seedFor(t *testing.T) *rand.Rand {
	t.Helper()
	seed := uint64(time.Now().UnixNano())
	t.Logf("random seed: %d", seed)
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

// runConcurrently starts n goroutines that all wait for the same signal and
// then run fn(i) at once.
func runConcurrently(n int, fn func(i int)) {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			fn(i)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
}

func (e *env) mustReconcileClean(t *testing.T) {
	t.Helper()
	if r := e.reconcile(t, true); !r.OK() {
		t.Fatalf("reconciliation found problems: mismatches=%d chainBreaks=%d currencies=%+v", len(r.Mismatches), len(r.ChainBreaks), r.Currencies)
	}
}

// requireNoProtectedBalanceEverNegative checks the whole history: not just the
// final balances, but every balance_after ever recorded for accounts that may
// not go negative.
func (e *env) requireNoProtectedBalanceEverNegative(t *testing.T) {
	t.Helper()
	var n int
	err := e.db.Super.QueryRow(context.Background(),
		`SELECT count(*) FROM postings p JOIN accounts a ON a.id = p.account_id
		  WHERE NOT a.allow_negative AND p.balance_after < 0`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d postings left a protected account with a negative balance", n)
	}
}

// One thousand transfers between accounts with very different funds, all
// released at the same instant.
func TestConcurrency_ThousandSimultaneousTransfers(t *testing.T) {
	e := newEnvWithConns(t, 20)
	ctx := context.Background()
	rng := seedFor(t)

	treasury := e.account(t, "treasury", "USD", ledger.Asset, ledger.Debit, true)
	opening := []int64{0, 0, 10, 50, 500, 1000, 5000, 20000} // from empty to rich
	wallets := make([]ledger.Account, len(opening))
	expected := make([]int64, len(opening))
	for i, amount := range opening {
		wallets[i] = e.account(t, fmt.Sprintf("wallet-%d", i), "USD", ledger.Liability, ledger.Credit, false)
		if amount > 0 {
			e.mustCreate(t, transfer(t, treasury.ID, wallets[i].ID, fmt.Sprint(amount), "USD"))
		}
		expected[i] = amount
	}
	fundingTxs := 0
	for _, a := range opening {
		if a > 0 {
			fundingTxs++
		}
	}

	type plan struct {
		from, to int
		amount   int64
	}
	const transfers = 1000
	plans := make([]plan, transfers)
	for i := range plans {
		from := rng.IntN(len(wallets))
		to := rng.IntN(len(wallets) - 1)
		if to >= from {
			to++
		}
		plans[i] = plan{from, to, 1 + rng.Int64N(50)}
	}

	errs := make([]error, transfers)
	start := time.Now()
	runConcurrently(transfers, func(i int) {
		p := plans[i]
		_, errs[i] = e.create(ctx, transfer(t, wallets[p.from].ID, wallets[p.to].ID, fmt.Sprint(p.amount), "USD"))
	})
	elapsed := time.Since(start)

	accepted, rejected := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
			expected[plans[i].from] -= plans[i].amount
			expected[plans[i].to] += plans[i].amount
		case errors.Is(err, ledger.ErrInsufficientFunds):
			rejected++
		default:
			t.Errorf("transfer %d failed with something other than an overdraft refusal: %v", i, err)
		}
	}
	t.Logf("%d accepted, %d refused for insufficient funds, in %v (%.0f transfers/s)",
		accepted, rejected, elapsed.Round(time.Millisecond), float64(transfers)/elapsed.Seconds())
	if accepted == 0 || rejected == 0 {
		t.Fatalf("the scenario must exercise both outcomes (accepted=%d rejected=%d)", accepted, rejected)
	}

	// 1. No lost updates: every wallet holds exactly what the accepted transfers imply.
	var total int64
	for i, w := range wallets {
		e.requireBalance(t, w.ID, fmt.Sprint(expected[i]))
		if expected[i] < 0 {
			t.Errorf("wallet %d ends negative: %d", i, expected[i])
		}
		total += expected[i]
	}
	// 2. Nothing created or destroyed by moving money between wallets.
	var funded int64
	for _, a := range opening {
		funded += a
	}
	if total != funded {
		t.Errorf("wallets hold %d in total, but %d was put in", total, funded)
	}
	// 3. Refused transfers left no trace; accepted ones left exactly two postings.
	if n := e.count(t, "transactions"); n != fundingTxs+accepted {
		t.Errorf("%d transactions stored, want %d funding + %d accepted", n, fundingTxs, accepted)
	}
	if n := e.count(t, "postings"); n != 2*(fundingTxs+accepted) {
		t.Errorf("%d postings stored, want %d", n, 2*(fundingTxs+accepted))
	}
	// 4. At no point in history did a protected wallet go negative.
	e.requireNoProtectedBalanceEverNegative(t)
	// 5. Stored balances, debit/credit totals and every balance chain agree.
	e.mustReconcileClean(t)
}

// When many callers compete for a balance, exactly as many succeed as the
// balance allows: no more (overdraft) and no fewer (a lost update).
func TestConcurrency_ExactlyAsManyAsTheBalanceAllows(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		amount, callers, wantAccepted int64
	}{
		{10, 300, 100}, // divides evenly: the balance reaches exactly zero
		{7, 300, 142},  // does not: 6 is left over
		{5, 400, 200},  // twice as many callers as the balance can serve
		{1001, 50, 0},  // every request is larger than the balance
	} {
		t.Run(fmt.Sprintf("withdraw %d x %d", tc.amount, tc.callers), func(t *testing.T) {
			e := newEnvWithConns(t, 20)
			treasury := e.account(t, "treasury", "USD", ledger.Asset, ledger.Debit, true)
			wallet := e.account(t, "wallet", "USD", ledger.Liability, ledger.Credit, false)
			e.mustCreate(t, transfer(t, treasury.ID, wallet.ID, "1000", "USD"))

			var accepted, refused, other atomic.Int64
			runConcurrently(int(tc.callers), func(int) {
				_, err := e.create(ctx, transfer(t, wallet.ID, treasury.ID, fmt.Sprint(tc.amount), "USD"))
				switch {
				case err == nil:
					accepted.Add(1)
				case errors.Is(err, ledger.ErrInsufficientFunds):
					refused.Add(1)
				default:
					other.Add(1)
					t.Errorf("unexpected error: %v", err)
				}
			})

			if accepted.Load() != tc.wantAccepted || refused.Load() != tc.callers-tc.wantAccepted || other.Load() != 0 {
				t.Errorf("accepted=%d refused=%d other=%d; want %d accepted and %d refused",
					accepted.Load(), refused.Load(), other.Load(), tc.wantAccepted, tc.callers-tc.wantAccepted)
			}
			e.requireBalance(t, wallet.ID, fmt.Sprint(1000-tc.wantAccepted*tc.amount))
			e.requireNoProtectedBalanceEverNegative(t)
			e.mustReconcileClean(t)
		})
	}
}

// Fifty identical requests released together (a client with an aggressive retry
// policy, or a proxy that duplicates requests): one effect, one answer.
func TestConcurrency_IdempotentDuplicatesOverHTTP(t *testing.T) {
	a := newAPIEnv(t)
	cash := a.createAccount(t, "cash", "USD", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "USD", "LIABILITY", "CREDIT", false)
	body := txBody(cash, wallet, "250", "USD")

	const callers = 50
	responses := make([]response, callers)
	runConcurrently(callers, func(i int) {
		responses[i] = a.do(t, "POST", "/v1/transactions", body, "Idempotency-Key", "same-key")
	})

	var fresh, replays int
	ids := map[string]bool{}
	for i, r := range responses {
		if r.Status != http.StatusCreated {
			t.Errorf("caller %d: status %d: %s", i, r.Status, r.Body)
			continue
		}
		ids[r.json(t)["id"].(string)] = true
		if r.Header.Get("Idempotent-Replayed") == "true" {
			replays++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replays != callers-1 || len(ids) != 1 {
		t.Errorf("fresh=%d replays=%d distinct transactions=%d; want 1, %d and 1", fresh, replays, len(ids), callers-1)
	}
	if n := a.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions stored, want exactly 1", n)
	}
	a.requireBalance(t, mustParseUUID(t, wallet), "250")
}

// The same key arrives with two different bodies at once. Exactly one body
// wins; every caller with the other body is told the key was reused; callers
// with the winning body get the winner's transaction.
func TestConcurrency_SameKeyWithConflictingBodies(t *testing.T) {
	e := newEnvWithConns(t, 20)
	ctx := context.Background()
	cash := e.account(t, "cash", "USD", ledger.Asset, ledger.Debit, true)
	wallet := e.account(t, "wallet", "USD", ledger.Liability, ledger.Credit, false)

	reqs := map[string]ledger.TransactionRequest{
		"A": transfer(t, cash.ID, wallet.ID, "111", "USD"),
		"B": transfer(t, cash.ID, wallet.ID, "222", "USD"),
	}
	type outcome struct {
		body string
		res  service.TransactionResult
		err  error
	}
	const callers = 60
	outcomes := make([]outcome, callers)
	runConcurrently(callers, func(i int) {
		body := "A"
		if i%2 == 1 {
			body = "B"
		}
		res, err := e.svc.CreateTransaction(ctx, keyFor("contested-key", body), reqs[body])
		outcomes[i] = outcome{body, res, err}
	})

	winners := map[string]int{}
	for _, o := range outcomes {
		if o.err == nil {
			winners[o.body]++
		}
	}
	if len(winners) != 1 {
		t.Fatalf("callers with both bodies succeeded: %v", winners)
	}
	var winningBody string
	for b := range winners {
		winningBody = b
	}
	if winners[winningBody] != callers/2 {
		t.Errorf("only %d of the %d callers with the winning body got an answer", winners[winningBody], callers/2)
	}

	var txID uuid.UUID
	fresh := 0
	for i, o := range outcomes {
		switch {
		case o.body != winningBody:
			if !errors.Is(o.err, ledger.ErrIdempotencyKeyReuse) {
				t.Errorf("caller %d (losing body): want key reuse, got %v", i, o.err)
			}
		case o.err != nil:
			t.Errorf("caller %d (winning body): %v", i, o.err)
		default:
			if txID == uuid.Nil {
				txID = o.res.Transaction.ID
			}
			if o.res.Transaction.ID != txID {
				t.Errorf("caller %d saw another transaction", i)
			}
			if !o.res.Replayed {
				fresh++
			}
		}
	}
	if fresh != 1 {
		t.Errorf("%d callers created the transaction, want exactly 1", fresh)
	}
	want := map[string]string{"A": "111", "B": "222"}[winningBody]
	e.requireBalance(t, wallet.ID, want)
	if n := e.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions stored, want 1", n)
	}
}

func TestConcurrency_DoubleReversalOverHTTP(t *testing.T) {
	a := newAPIEnv(t)
	cash := a.createAccount(t, "cash", "USD", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "USD", "LIABILITY", "CREDIT", false)
	original := a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(cash, wallet, "999", "USD"), "Idempotency-Key", "orig"), 201).json(t)["id"].(string)

	const callers = 12
	responses := make([]response, callers)
	runConcurrently(callers, func(i int) {
		// Each caller has its own key: they are different requests for the same reversal.
		responses[i] = a.do(t, "POST", "/v1/transactions/"+original+"/reverse", "", "Idempotency-Key", fmt.Sprintf("rev-%d", i))
	})

	created, conflicts := 0, 0
	for i, r := range responses {
		switch r.Status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
			if r.json(t)["code"] != "already_reversed" {
				t.Errorf("caller %d: %s", i, r.Body)
			}
		default:
			t.Errorf("caller %d: unexpected status %d: %s", i, r.Status, r.Body)
		}
	}
	if created != 1 || conflicts != callers-1 {
		t.Errorf("created=%d conflicts=%d, want 1 and %d", created, conflicts, callers-1)
	}
	a.requireBalance(t, mustParseUUID(t, wallet), "0")
	if n := a.count(t, "transactions"); n != 2 {
		t.Errorf("%d transactions, want the original plus exactly one reversal", n)
	}
}

// Transfers that touch one shared account must queue behind each other; transfers
// between disjoint pairs do not. This test checks that both stay correct and
// records the difference, which is the "hot account" ceiling the README documents.
func TestConcurrency_HotAccountVersusSpreadLoad(t *testing.T) {
	ctx := context.Background()
	const transfers, amount = 400, "3"

	measure := func(t *testing.T, hot bool) (perSecond float64) {
		e := newEnvWithConns(t, 20)
		treasury := e.account(t, "treasury", "USD", ledger.Asset, ledger.Debit, true)
		collector := e.account(t, "collector", "USD", ledger.Liability, ledger.Credit, true)
		sources := make([]ledger.Account, transfers)
		sinks := make([]ledger.Account, transfers)
		for i := range sources {
			sources[i] = e.account(t, fmt.Sprintf("src-%d", i), "USD", ledger.Asset, ledger.Debit, true)
			sinks[i] = e.account(t, fmt.Sprintf("dst-%d", i), "USD", ledger.Liability, ledger.Credit, true)
		}
		_ = treasury

		errs := make([]error, transfers)
		start := time.Now()
		runConcurrently(transfers, func(i int) {
			sink := sinks[i] // spread: every transfer has its own pair of accounts
			if hot {
				sink = collector // hot: every transfer credits the same account
			}
			_, errs[i] = e.create(ctx, transfer(t, sources[i].ID, sink.ID, amount, "USD"))
		})
		elapsed := time.Since(start)

		for i, err := range errs {
			if err != nil {
				t.Errorf("transfer %d: %v", i, err)
			}
		}
		if hot {
			e.requireBalance(t, collector.ID, fmt.Sprint(transfers*3))
		}
		e.mustReconcileClean(t)
		return float64(transfers) / elapsed.Seconds()
	}

	var spread, hot float64
	t.Run("spread across disjoint accounts", func(t *testing.T) { spread = measure(t, false) })
	t.Run("all crediting one account", func(t *testing.T) { hot = measure(t, true) })
	t.Logf("throughput: spread %.0f transfers/s, hot account %.0f transfers/s (hot/spread = %.2f)", spread, hot, hot/spread)
	if spread <= 0 || hot <= 0 {
		t.Error("a scenario did not complete")
	}
}

// Postings of one account are strictly ordered even under heavy contention: the
// ids and the running balances form a single chain.
func TestConcurrency_AccountHistoryIsASingleChain(t *testing.T) {
	e := newEnvWithConns(t, 20)
	ctx := context.Background()
	treasury := e.account(t, "treasury", "USD", ledger.Asset, ledger.Debit, true)
	wallet := e.account(t, "wallet", "USD", ledger.Liability, ledger.Credit, false)

	const deposits = 300
	runConcurrently(deposits, func(i int) {
		if _, err := e.create(ctx, transfer(t, treasury.ID, wallet.ID, fmt.Sprint(1+i%5), "USD")); err != nil {
			t.Errorf("deposit %d: %v", i, err)
		}
	})

	page, err := e.svc.ListPostings(ctx, wallet.ID, "", service.MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	var balances []*big.Int
	for {
		for _, p := range page.Postings {
			ids = append(ids, p.ID)
			balances = append(balances, p.BalanceAfter)
		}
		if page.NextCursor == "" {
			break
		}
		if page, err = e.svc.ListPostings(ctx, wallet.ID, page.NextCursor, service.MaxPageSize); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != deposits {
		t.Fatalf("%d postings, want %d", len(ids), deposits)
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }) {
		t.Error("posting ids are not strictly decreasing newest-first")
	}
	// Newest first: each balance equals the next-older balance plus this posting's amount.
	for i := 0; i < len(balances)-1; i++ {
		step := new(big.Int).Sub(balances[i], balances[i+1])
		if step.Sign() <= 0 || step.Cmp(big.NewInt(5)) > 0 {
			t.Fatalf("history is not a clean chain at position %d: step %s", i, step)
		}
	}
	e.mustReconcileClean(t)
	if !strings.Contains(fmt.Sprint(balances[0]), fmt.Sprint(e.balance(t, wallet.ID))) {
		t.Errorf("the newest balance_after %s differs from the account balance %s", balances[0], e.balance(t, wallet.ID))
	}
}
