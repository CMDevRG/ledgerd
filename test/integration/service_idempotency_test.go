package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
)

// keyFor builds an idempotency identity from a client key and a request
// fingerprint, the way the HTTP layer will.
func keyFor(key, body string) service.Idempotency {
	return service.Idempotency{Key: key, RequestHash: service.RequestHash("POST", "/v1/transactions", []byte(body))}
}

func (e *env) storedKeys(t *testing.T) int { t.Helper(); return e.count(t, "idempotency_keys") }

func TestIdempotency_FirstCallStoresTheKeyWithItsResult(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	res, err := e.svc.CreateTransaction(ctx, keyFor("k-1", "body"), transfer(t, cash.ID, wallet.ID, "100", "USD"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed {
		t.Error("the first call is not a replay")
	}

	var status int
	var txID uuid.UUID
	var body string
	if err := e.db.Super.QueryRow(ctx,
		`SELECT response_status, transaction_id, response_body::text FROM idempotency_keys WHERE key = 'k-1'`).Scan(&status, &txID, &body); err != nil {
		t.Fatal(err)
	}
	if status != 201 || txID != res.Transaction.ID {
		t.Errorf("stored status=%d transaction=%s, want 201 and %s", status, txID, res.Transaction.ID)
	}
	if want := fmt.Sprintf(`{"transaction_id": "%s"}`, res.Transaction.ID); body != want {
		t.Errorf("stored body = %s, want %s", body, want)
	}
}

func TestIdempotency_ReplayReturnsTheOriginalWithoutANewEffect(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	idem := keyFor("k-replay", "same body")
	req := transfer(t, cash.ID, wallet.ID, "100", "USD")

	first, err := e.svc.CreateTransaction(ctx, idem, req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := e.svc.CreateTransaction(ctx, idem, req)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if !again.Replayed {
			t.Errorf("replay %d must be flagged as a replay", i)
		}
		if again.Transaction.ID != first.Transaction.ID || len(again.Transaction.Postings) != 2 ||
			again.Transaction.Postings[0].ID != first.Transaction.Postings[0].ID ||
			!again.Transaction.CreatedAt.Equal(first.Transaction.CreatedAt) {
			t.Errorf("replay %d returned a different transaction: %+v vs %+v", i, again.Transaction, first.Transaction)
		}
	}

	if n := e.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions, want exactly 1", n)
	}
	e.requireBalance(t, cash.ID, "100") // not 400
	e.requireBalance(t, wallet.ID, "100")
}

func TestIdempotency_SameKeyDifferentRequestIsRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	if _, err := e.svc.CreateTransaction(ctx, keyFor("k-reuse", "body A"), transfer(t, cash.ID, wallet.ID, "100", "USD")); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateTransaction(ctx, keyFor("k-reuse", "body B"), transfer(t, cash.ID, wallet.ID, "999", "USD"))
	if !errors.Is(err, ledger.ErrIdempotencyKeyReuse) {
		t.Fatalf("want ErrIdempotencyKeyReuse, got %v", err)
	}
	if n := e.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions, want 1", n)
	}
	e.requireBalance(t, wallet.ID, "100")
}

func TestIdempotency_AFailedAttemptDoesNotConsumeTheKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	idem := keyFor("k-retry-after-failure", "withdraw 80")
	withdraw := transfer(t, wallet.ID, cash.ID, "80", "USD") // the wallet is empty: overdraft

	if _, err := e.svc.CreateTransaction(ctx, idem, withdraw); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("want insufficient funds, got %v", err)
	}
	if n := e.storedKeys(t); n != 0 {
		t.Fatalf("a rejected request left %d idempotency keys behind", n)
	}

	// Money arrives; the very same request (same key, same fingerprint) now succeeds.
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))
	res, err := e.svc.CreateTransaction(ctx, idem, withdraw)
	if err != nil {
		t.Fatalf("retry with the same key should be processed normally: %v", err)
	}
	if res.Replayed {
		t.Error("the earlier attempt never completed, so this is not a replay")
	}
	e.requireBalance(t, wallet.ID, "20")
}

func TestIdempotency_ValidationErrorsAreNotStored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	idem := keyFor("k-fix-and-retry", "x")

	bad := transfer(t, cash.ID, wallet.ID, "100", "USD")
	bad.Postings[1].Amount = amt(t, "99") // unbalanced
	if _, err := e.svc.CreateTransaction(ctx, idem, bad); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
	if n := e.storedKeys(t); n != 0 {
		t.Fatalf("%d keys stored for an invalid request", n)
	}
	// Fixing the request and reusing the key works.
	if _, err := e.svc.CreateTransaction(ctx, idem, transfer(t, cash.ID, wallet.ID, "100", "USD")); err != nil {
		t.Fatal(err)
	}
}

func TestIdempotency_KeyAndHashAreValidated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	req := transfer(t, cash.ID, wallet.ID, "1", "USD")
	goodHash := service.RequestHash("POST", "/v1/transactions", nil)

	tests := []struct {
		name string
		idem service.Idempotency
	}{
		{"empty key", service.Idempotency{Key: "", RequestHash: goodHash}},
		{"key with a space", service.Idempotency{Key: "has space", RequestHash: goodHash}},
		{"key with a newline", service.Idempotency{Key: "line\nbreak", RequestHash: goodHash}},
		{"non-ASCII key", service.Idempotency{Key: "clé", RequestHash: goodHash}},
		{"key too long", service.Idempotency{Key: string(make([]byte, 256)), RequestHash: goodHash}},
		{"missing hash", service.Idempotency{Key: "ok"}},
		{"short hash", service.Idempotency{Key: "ok", RequestHash: []byte{1, 2, 3}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.CreateTransaction(ctx, tc.idem, req)
			var ve *ledger.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
		})
	}

	// The longest allowed key is accepted.
	long := make([]byte, 255)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := e.svc.CreateTransaction(ctx, service.Idempotency{Key: string(long), RequestHash: goodHash}, req); err != nil {
		t.Errorf("a 255-character key must be accepted: %v", err)
	}
	e.requireNothingPersistedExcept(t, 1)
}

func TestIdempotency_RequestHashCoversMethodPathAndBody(t *testing.T) {
	a := service.RequestHash("POST", "/v1/transactions", []byte(`{"a":1}`))
	if string(a) != string(service.RequestHash("POST", "/v1/transactions", []byte(`{"a":1}`))) {
		t.Error("the hash must be deterministic")
	}
	for name, other := range map[string][]byte{
		"different body":   service.RequestHash("POST", "/v1/transactions", []byte(`{"a":2}`)),
		"different path":   service.RequestHash("POST", "/v1/transactions/x/reverse", []byte(`{"a":1}`)),
		"different method": service.RequestHash("PUT", "/v1/transactions", []byte(`{"a":1}`)),
		// Without separators, ("ab","c") and ("a","bc") would collide.
		"boundary shift": service.RequestHash("POS", "T/v1/transactions", []byte(`{"a":1}`)),
	} {
		if string(a) == string(other) {
			t.Errorf("%s must change the hash", name)
		}
	}
}

func TestIdempotency_Reversals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	t1 := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))
	t2 := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "50", "USD"))

	hashFor := func(id uuid.UUID) []byte {
		return service.RequestHash("POST", "/v1/transactions/"+id.String()+"/reverse", nil)
	}
	idem1 := service.Idempotency{Key: "rev-1", RequestHash: hashFor(t1.ID)}

	first, err := e.svc.Reverse(ctx, idem1, t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.Reverse(ctx, idem1, t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Transaction.ID != first.Transaction.ID {
		t.Errorf("replayed reversal = %+v, want the original reversal %s", again, first.Transaction.ID)
	}
	if n := e.count(t, "transactions"); n != 3 {
		t.Errorf("%d transactions, want 3 (two originals + one reversal)", n)
	}

	// The same key for a different transaction is a different request.
	_, err = e.svc.Reverse(ctx, service.Idempotency{Key: "rev-1", RequestHash: hashFor(t2.ID)}, t2.ID)
	if !errors.Is(err, ledger.ErrIdempotencyKeyReuse) {
		t.Fatalf("want ErrIdempotencyKeyReuse, got %v", err)
	}

	// A new key for an already reversed transaction is "already reversed", and stores nothing.
	before := e.storedKeys(t)
	_, err = e.svc.Reverse(ctx, service.Idempotency{Key: "rev-1b", RequestHash: hashFor(t1.ID)}, t1.ID)
	if !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("want ErrAlreadyReversed, got %v", err)
	}
	if e.storedKeys(t) != before {
		t.Error("a rejected reversal must not store its key")
	}
}

// Many identical requests arrive at the same moment (a client retrying
// aggressively, or a network proxy duplicating a request). Exactly one
// transaction must exist afterwards and every caller must see that same one.
func TestIdempotency_ConcurrentDuplicatesHaveExactlyOneEffect(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	idem := keyFor("k-concurrent", "deposit")
	req := transfer(t, cash.ID, wallet.ID, "100", "USD")

	const callers = 50
	type outcome struct {
		res service.TransactionResult
		err error
	}
	outcomes := make([]outcome, callers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			res, err := e.svc.CreateTransaction(ctx, idem, req)
			outcomes[i] = outcome{res, err}
		}()
	}
	start.Done()
	done.Wait()

	var fresh, replays atomic.Int32
	var id uuid.UUID
	for i, o := range outcomes {
		if o.err != nil {
			t.Errorf("caller %d failed: %v", i, o.err)
			continue
		}
		if id == uuid.Nil {
			id = o.res.Transaction.ID
		}
		if o.res.Transaction.ID != id {
			t.Errorf("caller %d saw transaction %s, others saw %s", i, o.res.Transaction.ID, id)
		}
		if o.res.Replayed {
			replays.Add(1)
		} else {
			fresh.Add(1)
		}
	}
	if fresh.Load() != 1 || replays.Load() != callers-1 {
		t.Errorf("fresh=%d replayed=%d, want 1 and %d", fresh.Load(), replays.Load(), callers-1)
	}
	if n := e.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions, want exactly 1", n)
	}
	e.requireBalance(t, cash.ID, "100")
	e.requireBalance(t, wallet.ID, "100")
}

func TestIdempotency_Cleanup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	e.clock.Set(now)

	for key, age := range map[string]time.Duration{"old-72h": 72 * time.Hour, "mid-30h": 30 * time.Hour, "fresh-1h": time.Hour} {
		if _, err := e.db.Super.Exec(ctx,
			`INSERT INTO idempotency_keys (key, request_hash, response_status, response_body, created_at)
			 VALUES ($1, '\x00', 201, '{}', $2)`, key, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := e.svc.PurgeIdempotencyKeys(ctx, 23*time.Hour); err == nil {
		t.Error("a retention below 24h must be refused")
	}
	if n := e.storedKeys(t); n != 3 {
		t.Fatalf("a refused purge deleted rows: %d left", n)
	}

	n, err := e.svc.PurgeIdempotencyKeys(ctx, 48*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("purge(48h) = %d, %v; want exactly the 72h-old key", n, err)
	}
	n, err = e.svc.PurgeIdempotencyKeys(ctx, 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("purge(24h) = %d, %v; want the 30h-old key", n, err)
	}
	var left string
	if err := e.db.Super.QueryRow(ctx, `SELECT key FROM idempotency_keys`).Scan(&left); err != nil || left != "fresh-1h" {
		t.Fatalf("remaining key = %q, %v; want only fresh-1h", left, err)
	}
}

func TestIdempotency_CleanupLoopRunsUntilCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().UTC()
	e.clock.Set(now)
	if _, err := e.db.Super.Exec(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, response_status, response_body, created_at)
		 VALUES ('stale', '\x00', 201, '{}', $1)`, now.Add(-100*time.Hour)); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		e.svc.RunIdempotencyCleanup(ctx, 48*time.Hour, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(stopped)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for e.storedKeys(t) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the cleanup loop never removed the stale key")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the cleanup loop did not stop after the context was cancelled")
	}
}

func TestIdempotency_RetentionMinimumMatchesConfiguration(t *testing.T) {
	if service.MinIdempotencyRetention != config.MinIdempotencyTTL {
		t.Errorf("service minimum %v differs from the configuration minimum %v", service.MinIdempotencyRetention, config.MinIdempotencyTTL)
	}
}
