package integration

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/store"
	"github.com/CMDevRG/ledgerd/internal/store/db"
)

func newAccount(t *testing.T, st *store.Store, code string) uuid.UUID {
	t.Helper()
	a, err := st.CreateAccount(context.Background(), ledger.NewAccount{
		Code: code, Currency: "USD", Type: ledger.Asset, NormalBalance: ledger.Debit, AllowNegative: true,
	})
	if err != nil {
		t.Fatalf("create account %s: %v", code, err)
	}
	return a.ID
}

var errSentinel = errors.New("sentinel failure")

func TestInTx_CommitsOnSuccessAndRollsBackOnError(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})

	// Success: the row is visible afterwards.
	err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
		_, err := q.CreateAccount(ctx, db.CreateAccountParams{Code: "kept", Currency: "USD", Type: "ASSET", NormalBalance: "DEBIT"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Failure after a successful insert: nothing must remain, and the error is
	// returned unchanged (not retried, not wrapped as "unavailable").
	calls := 0
	err = st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
		calls++
		if _, err := q.CreateAccount(ctx, db.CreateAccountParams{Code: "discarded", Currency: "USD", Type: "ASSET", NormalBalance: "DEBIT"}); err != nil {
			return err
		}
		return errSentinel
	})
	if !errors.Is(err, errSentinel) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want the sentinel after exactly one call", err, calls)
	}

	var n int
	if err := database.Super.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE code IN ('kept','discarded')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows present, want only the committed one", n)
	}
}

// lock_timeout is applied with SET LOCAL, so it must disappear when the
// transaction ends instead of staying on the pooled connection. A pool with a
// single connection guarantees that the check below reuses the same session.
func TestInTx_LockTimeoutDoesNotLeakToThePool(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(database.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	st := store.New(pool, store.Config{LockTimeout: 1234 * time.Millisecond})
	if err := st.InTx(ctx, func(context.Context, *db.Queries) error { return nil }); err != nil {
		t.Fatal(err)
	}

	var after string
	if err := pool.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != "0" {
		t.Errorf("lock_timeout = %q after the transaction, want the default \"0\"", after)
	}
}

func TestInTx_LockTimeout(t *testing.T) {
	database := server.NewDB(t)
	// Safety net: if lock_timeout were not applied the call below would wait
	// for the holder forever; this makes the test fail instead of hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := store.New(database.App, store.Config{LockTimeout: 200 * time.Millisecond})
	id := newAccount(t, st, "contended")

	// Another session holds the row lock and does not let go.
	holder, err := database.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Registered after NewDB's cleanup, so it runs before the pools are closed
	// (a pool cannot close while a transaction still holds a connection).
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}

	calls := 0
	start := time.Now()
	err = st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
		calls++
		_, err := store.LockAccounts(ctx, q, []uuid.UUID{id})
		return err
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ledger.ErrTemporarilyUnavailable) {
		t.Fatalf("want ErrTemporarilyUnavailable, got %v", err)
	}
	requirePgError(t, err, "55P03", "")
	if calls != 1 {
		t.Errorf("a lock timeout must not be retried inside the store: %d calls", calls)
	}
	if elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("returned after %v, want about the 200ms lock timeout", elapsed)
	}

	// Once the lock is released the same call works.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
		_, err := store.LockAccounts(ctx, q, []uuid.UUID{id})
		return err
	}); err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
}

// oppositeOrder makes two transactions lock the same two accounts in opposite
// order, after both hold their first lock, which forces a deadlock on the
// first attempt. It returns the error of each transaction and the total number
// of attempts made.
func oppositeOrder(t *testing.T, st *store.Store, x, y uuid.UUID) (errs [2]error, attempts int32) {
	t.Helper()
	ctx := context.Background()

	var bothHoldFirst sync.WaitGroup
	bothHoldFirst.Add(2)
	var total atomic.Int32
	var wg sync.WaitGroup

	for i, order := range [2][2]uuid.UUID{{x, y}, {y, x}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			errs[i] = st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
				total.Add(1)
				// Lock one account at a time, in the order given (NOT sorted).
				if _, err := store.LockAccounts(ctx, q, order[:1]); err != nil {
					return err
				}
				if first {
					first = false
					bothHoldFirst.Done()
					bothHoldFirst.Wait()
				}
				_, err := store.LockAccounts(ctx, q, order[1:])
				return err
			})
		}()
	}
	wg.Wait()
	return errs, total.Load()
}

func TestInTx_RetriesARealDeadlock(t *testing.T) {
	database := server.NewDB(t)
	st := store.New(database.App, store.Config{MaxAttempts: 4, BackoffBase: 5 * time.Millisecond, LockTimeout: 20 * time.Second})
	x, y := newAccount(t, st, "x"), newAccount(t, st, "y")

	errs, attempts := oppositeOrder(t, st, x, y)

	for i, err := range errs {
		if err != nil {
			t.Errorf("transaction %d failed despite retries: %v", i, err)
		}
	}
	// PostgreSQL aborts exactly one of the two with a deadlock; that one runs
	// again and succeeds: 2 first attempts + 1 retry.
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (one deadlock victim retried once)", attempts)
	}
}

func TestInTx_ReportsDeadlockWhenRetriesAreExhausted(t *testing.T) {
	database := server.NewDB(t)
	st := store.New(database.App, store.Config{MaxAttempts: 1, LockTimeout: 20 * time.Second}) // no retries
	x, y := newAccount(t, st, "x"), newAccount(t, st, "y")

	errs, _ := oppositeOrder(t, st, x, y)

	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			if !errors.Is(err, ledger.ErrTemporarilyUnavailable) {
				t.Errorf("want ErrTemporarilyUnavailable, got %v", err)
			}
			requirePgError(t, err, "40P01", "")
		}
	}
	if failed != 1 {
		t.Errorf("%d transactions failed, want exactly the one deadlock victim", failed)
	}
}

// With LockAccounts, transactions that need overlapping sets of accounts in
// any order never deadlock. MaxAttempts is 1, so a single deadlock would show
// up as an error instead of being quietly retried.
func TestLockAccounts_NeverDeadlocksWhenIdsArriveInAnyOrder(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{MaxAttempts: 1, LockTimeout: 20 * time.Second})

	const accountsN = 6
	ids := make([]uuid.UUID, accountsN)
	for i := range ids {
		ids[i] = newAccount(t, st, "acct-"+string(rune('a'+i)))
	}

	const workers, rounds = 24, 15
	var wg sync.WaitGroup
	failures := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				perm := rand.Perm(accountsN)[:3+rand.IntN(accountsN-2)] //nolint:gosec // test shuffling
				pick := make([]uuid.UUID, len(perm))
				for i, p := range perm {
					pick[i] = ids[p]
				}
				err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
					if _, err := store.LockAccounts(ctx, q, pick); err != nil {
						return err
					}
					time.Sleep(time.Millisecond) // hold the locks long enough to force contention
					return nil
				})
				if err != nil {
					failures <- err
				}
			}
		}()
	}
	wg.Wait()
	close(failures)

	for err := range failures {
		t.Errorf("transaction failed: %v", err)
	}
}
