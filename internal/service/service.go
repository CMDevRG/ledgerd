// Package service orchestrates the ledger use cases: it validates requests,
// opens a database transaction, locks accounts, applies the pure domain rules
// and records the result.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/store"
)

// Service implements the ledger use cases.
type Service struct {
	store *store.Store
	now   func() time.Time
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces the time source (used by tests to control timestamps).
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// New builds a Service.
func New(st *store.Store, opts ...Option) *Service {
	s := &Service{store: st, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateAccount validates and creates an account.
func (s *Service) CreateAccount(ctx context.Context, n ledger.NewAccount) (ledger.Account, error) {
	if err := n.Validate(); err != nil {
		return ledger.Account{}, err
	}
	return s.store.CreateAccount(ctx, n)
}

// GetAccount returns an account with its current balance.
func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (ledger.Account, error) {
	return s.store.GetAccount(ctx, id)
}

// GetTransaction returns a transaction with its postings.
func (s *Service) GetTransaction(ctx context.Context, id uuid.UUID) (ledger.Transaction, error) {
	return s.store.GetTransaction(ctx, id)
}

// createdStatus is the HTTP status stored with a completed idempotency key.
const createdStatus = 201

// TransactionResult is the outcome of a write.
type TransactionResult struct {
	Transaction ledger.Transaction
	// Replayed is true when the key had already been used with the same
	// request and the original transaction is being returned again.
	Replayed bool
}

// CreateTransaction validates a request and records it atomically, exactly
// once per idempotency key. Validation errors are returned before anything is
// stored, so a client can fix the request and reuse the key.
func (s *Service) CreateTransaction(ctx context.Context, idem Idempotency, req ledger.TransactionRequest) (TransactionResult, error) {
	if err := idem.validate(); err != nil {
		return TransactionResult{}, err
	}
	if err := req.Validate(); err != nil {
		return TransactionResult{}, err
	}
	return s.idempotent(ctx, idem, func(ctx context.Context, tx *store.Tx) (ledger.Transaction, error) {
		return s.post(ctx, tx, req, nil)
	})
}

// Reverse records a new transaction that offsets an existing one: the same
// accounts and amounts with the opposite directions. The original is never
// modified. A transaction can be reversed once, and the reversal is subject to
// the same rules as any other transaction (undoing a deposit that has already
// been spent is rejected by the overdraft check). Like CreateTransaction it
// takes effect once per idempotency key.
func (s *Service) Reverse(ctx context.Context, idem Idempotency, id uuid.UUID) (TransactionResult, error) {
	if err := idem.validate(); err != nil {
		return TransactionResult{}, err
	}
	return s.idempotent(ctx, idem, func(ctx context.Context, tx *store.Tx) (ledger.Transaction, error) {
		original, err := tx.LoadTransaction(ctx, id)
		if err != nil {
			return ledger.Transaction{}, err
		}
		rev, err := ledger.Reverse(original)
		if err != nil {
			return ledger.Transaction{}, err
		}
		return s.post(ctx, tx, rev.Request, &rev.ReversesID)
	})
}

// post applies a validated request inside an open transaction: lock the
// accounts, apply the rules in memory, write the result.
func (s *Service) post(ctx context.Context, tx *store.Tx, req ledger.TransactionRequest, reverses *uuid.UUID) (ledger.Transaction, error) {
	ids := make([]uuid.UUID, len(req.Postings))
	for i, p := range req.Postings {
		ids[i] = p.AccountID
	}
	accounts, err := tx.LockAccounts(ctx, ids)
	if err != nil {
		return ledger.Transaction{}, err
	}

	// A reversal is checked AFTER the locks are held. If two requests race to
	// reverse the same transaction, the loser waits for the winner's locks and
	// then sees the committed reversal here, so it is told "already reversed"
	// rather than failing the overdraft check against the balances the winner
	// left behind. (The unique constraint on reverses_id remains the final guard.)
	if reverses != nil {
		existing, err := tx.ReversalOf(ctx, *reverses)
		if err != nil {
			return ledger.Transaction{}, err
		}
		if existing != nil {
			return ledger.Transaction{}, ledger.ErrAlreadyReversed
		}
	}

	applied, err := ledger.Apply(accounts, req)
	if err != nil {
		return ledger.Transaction{}, err
	}

	t, err := tx.Record(ctx, store.Recording{
		Description: req.Description,
		ReversesID:  reverses,
		RecordedAt:  s.recordedAt(accounts),
		Postings:    applied.Postings,
		NewBalances: applied.NewBalances,
	})
	if err != nil {
		return ledger.Transaction{}, fmt.Errorf("record transaction: %w", err)
	}
	return t, nil
}

// recordedAt returns the timestamp shared by a transaction and its postings.
//
// It is the current time, but never earlier than the last posting of any
// account involved. Since every write to an account happens under that
// account's row lock, this keeps recorded_at non-decreasing per account even if
// the clock steps backwards or application servers disagree slightly, which is
// what makes "balance as of T" and keyset pagination consistent. It is
// truncated to microseconds, PostgreSQL's resolution, so what we compare in Go
// equals what is stored.
func (s *Service) recordedAt(locked map[uuid.UUID]ledger.Account) time.Time {
	t := s.now().UTC().Truncate(time.Microsecond)
	for _, a := range locked {
		if a.LastPostingAt != nil && a.LastPostingAt.After(t) {
			t = *a.LastPostingAt
		}
	}
	return t
}
