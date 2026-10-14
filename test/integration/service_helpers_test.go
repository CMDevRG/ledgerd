package integration

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/service"
	"github.com/CMDevRG/ledgerd/internal/store"
	"github.com/CMDevRG/ledgerd/internal/testdb"
)

// fakeClock is a settable time source.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 123456789, time.UTC)} // has nanoseconds on purpose
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// env bundles a fresh database with a store, a service and a controllable clock.
type env struct {
	db    *testdb.DB
	st    *store.Store
	svc   *service.Service
	clock *fakeClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	database := server.NewDB(t)
	clock := newFakeClock()
	st := store.New(database.App, store.Config{})
	return &env{db: database, st: st, svc: service.New(st, service.WithClock(clock.Now)), clock: clock}
}

func (e *env) account(t *testing.T, code, currency string, typ ledger.AccountType, normal ledger.Side, allowNegative bool) ledger.Account {
	t.Helper()
	a, err := e.svc.CreateAccount(context.Background(), ledger.NewAccount{
		Code: code, Currency: ledger.Currency(currency), Type: typ, NormalBalance: normal, AllowNegative: allowNegative,
	})
	if err != nil {
		t.Fatalf("create account %s: %v", code, err)
	}
	return a
}

// cashAndWallet creates the classic pair: a debit-normal asset (bank cash) and
// a credit-normal liability (user wallet), both protected against overdraft.
func (e *env) cashAndWallet(t *testing.T) (cash, wallet ledger.Account) {
	t.Helper()
	return e.account(t, "bank-cash", "USD", ledger.Asset, ledger.Debit, false),
		e.account(t, "user-wallet", "USD", ledger.Liability, ledger.Credit, false)
}

func amt(t testing.TB, s string) ledger.Amount {
	t.Helper()
	a, err := ledger.ParseAmount(s)
	if err != nil {
		t.Fatalf("amount %q: %v", s, err)
	}
	return a
}

func in(t testing.TB, account uuid.UUID, side ledger.Side, amount, currency string) ledger.PostingInput {
	t.Helper()
	return ledger.PostingInput{AccountID: account, Direction: side, Amount: amt(t, amount), Currency: ledger.Currency(currency)}
}

// transfer builds a two-posting request: debit one account, credit another.
func transfer(t testing.TB, debit, credit uuid.UUID, amount, currency string) ledger.TransactionRequest {
	t.Helper()
	return ledger.TransactionRequest{Postings: []ledger.PostingInput{
		in(t, debit, ledger.Debit, amount, currency),
		in(t, credit, ledger.Credit, amount, currency),
	}}
}

// freshKey returns an idempotency key that has never been used.
func freshKey() service.Idempotency {
	key := uuid.NewString()
	return service.Idempotency{Key: key, RequestHash: service.RequestHash("TEST", key, nil)}
}

// create submits a transaction with a fresh idempotency key.
func (e *env) create(ctx context.Context, req ledger.TransactionRequest) (ledger.Transaction, error) {
	res, err := e.svc.CreateTransaction(ctx, freshKey(), req)
	return res.Transaction, err
}

// reverse reverses a transaction with a fresh idempotency key.
func (e *env) reverse(ctx context.Context, id uuid.UUID) (ledger.Transaction, error) {
	res, err := e.svc.Reverse(ctx, freshKey(), id)
	return res.Transaction, err
}

func (e *env) mustCreate(t *testing.T, req ledger.TransactionRequest) ledger.Transaction {
	t.Helper()
	tx, err := e.create(context.Background(), req)
	if err != nil {
		t.Fatalf("transaction should have been accepted: %v", err)
	}
	return tx
}

func (e *env) balance(t *testing.T, id uuid.UUID) *big.Int {
	t.Helper()
	a, err := e.svc.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return a.Balance
}

func (e *env) requireBalance(t *testing.T, id uuid.UUID, want string) {
	t.Helper()
	if got := e.balance(t, id).String(); got != want {
		t.Errorf("balance of %s = %s, want %s", id, got, want)
	}
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.db.Super.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// requireNothingPersisted asserts that no transaction or posting exists.
func (e *env) requireNothingPersisted(t *testing.T) {
	t.Helper()
	if n := e.count(t, "transactions"); n != 0 {
		t.Errorf("%d transactions persisted, want 0", n)
	}
	if n := e.count(t, "postings"); n != 0 {
		t.Errorf("%d postings persisted, want 0", n)
	}
}

// requireNothingPersistedExcept asserts the number of transactions.
func (e *env) requireNothingPersistedExcept(t *testing.T, transactions int) {
	t.Helper()
	if n := e.count(t, "transactions"); n != transactions {
		t.Errorf("%d transactions persisted, want %d", n, transactions)
	}
}
