package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/pgnum"
	"github.com/CMDevRG/ledgerd/internal/store/db"
)

// Tx is the set of ledger operations available inside a database transaction.
// It speaks the domain's language (ledger types) so that the service layer
// does not depend on the generated query code.
type Tx struct {
	q *db.Queries
}

// Do runs fn in a transaction with the guarantees described on InTx
// (commit/rollback, lock timeout, deadlock retry). Because fn can run more
// than once, it must reset any state it captures from the outside.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	return s.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
		return fn(ctx, &Tx{q: q})
	})
}

// LockAccounts locks the accounts in a fixed order and returns their state.
// See the package-level LockAccounts for details.
func (t *Tx) LockAccounts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ledger.Account, error) {
	return LockAccounts(ctx, t.q, ids)
}

// LoadTransaction returns a transaction with its postings in insertion order,
// or a *ledger.TransactionNotFoundError.
func (t *Tx) LoadTransaction(ctx context.Context, id uuid.UUID) (ledger.Transaction, error) {
	return loadTransaction(ctx, t.q, id)
}

func loadTransaction(ctx context.Context, q *db.Queries, id uuid.UUID) (ledger.Transaction, error) {
	row, err := q.GetTransaction(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ledger.Transaction{}, &ledger.TransactionNotFoundError{ID: id}
		}
		return ledger.Transaction{}, fmt.Errorf("get transaction: %w", err)
	}
	postings, err := q.ListTransactionPostings(ctx, id)
	if err != nil {
		return ledger.Transaction{}, fmt.Errorf("list transaction postings: %w", err)
	}
	return transactionToDomain(row, postings)
}

// ReversalOf returns the id of the transaction that reverses id, or nil.
func (t *Tx) ReversalOf(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	row, err := t.q.GetReversalOf(ctx, &id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get reversal: %w", err)
	}
	return &row.ID, nil
}

// Recording describes a transaction that has been validated and applied in
// memory and is ready to be written.
type Recording struct {
	Description string
	ReversesID  *uuid.UUID
	// RecordedAt is the single timestamp shared by the transaction and all its postings.
	RecordedAt time.Time
	// Postings are the applied postings in request order.
	Postings []ledger.AppliedPosting
	// NewBalances holds the final balance of every touched account.
	NewBalances map[uuid.UUID]*big.Int
}

// Record writes the transaction, its postings and the new account balances.
// All three are part of the surrounding database transaction, so they become
// visible together or not at all. The accounts must already be locked.
func (t *Tx) Record(ctx context.Context, r Recording) (ledger.Transaction, error) {
	txRow, err := t.q.InsertTransaction(ctx, db.InsertTransactionParams{
		Description: r.Description,
		ReversesID:  r.ReversesID,
		CreatedAt:   r.RecordedAt,
	})
	if err != nil {
		if pgErr := pgError(err); pgErr != nil && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "transactions_reverses_id_key" {
			return ledger.Transaction{}, ledger.ErrAlreadyReversed
		}
		return ledger.Transaction{}, fmt.Errorf("insert transaction: %w", err)
	}

	params := make([]db.InsertPostingParams, len(r.Postings))
	for i, p := range r.Postings {
		params[i] = db.InsertPostingParams{
			TransactionID: txRow.ID,
			AccountID:     p.AccountID,
			Currency:      string(p.Currency),
			Direction:     string(p.Direction),
			Amount:        pgnum.FromBig(p.Amount.Big()),
			BalanceAfter:  pgnum.FromBig(p.BalanceAfter),
			RecordedAt:    r.RecordedAt,
		}
	}
	inserted := make([]db.Posting, 0, len(params))
	var firstErr error
	t.q.InsertPosting(ctx, params).QueryRow(func(_ int, p db.Posting, err error) {
		if err != nil {
			// Keep the first (root-cause) error; later ones are "transaction aborted".
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		inserted = append(inserted, p)
	})
	if firstErr != nil {
		return ledger.Transaction{}, fmt.Errorf("insert postings: %w", firstErr)
	}

	// Update balances in ascending id order (deterministic statement contents).
	ids := make([]uuid.UUID, 0, len(r.NewBalances))
	for id := range r.NewBalances {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return lessUUID(ids[i], ids[j]) })
	balances := make([]pgnum.Int, len(ids))
	for i, id := range ids {
		balances[i] = pgnum.FromBig(r.NewBalances[id])
	}
	if err := t.q.UpdateAccountBalances(ctx, db.UpdateAccountBalancesParams{
		RecordedAt: r.RecordedAt, Ids: ids, Balances: balances,
	}); err != nil {
		return ledger.Transaction{}, fmt.Errorf("update balances: %w", err)
	}

	return transactionToDomain(txRow, inserted)
}

func lessUUID(a, b uuid.UUID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// GetTransaction returns a committed transaction with its postings. Both are
// immutable once committed, so reading them with two statements is consistent.
func (s *Store) GetTransaction(ctx context.Context, id uuid.UUID) (ledger.Transaction, error) {
	return loadTransaction(ctx, s.Queries(), id)
}

// ClaimIdempotencyKey registers the key for the current transaction. It reports
// false if the key is already taken by a committed request.
//
// If another in-flight transaction has claimed the same key but not yet
// committed, this call blocks until that transaction finishes (PostgreSQL
// waits on the unique index entry): afterwards the answer is "taken" if it
// committed and "free" if it rolled back. That wait is what turns concurrent
// duplicates into exactly one effect. The wait is bounded by lock_timeout.
func (t *Tx) ClaimIdempotencyKey(ctx context.Context, key string, requestHash []byte) (bool, error) {
	_, err := t.q.ClaimIdempotencyKey(ctx, db.ClaimIdempotencyKeyParams{Key: key, RequestHash: requestHash})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim idempotency key: %w", err)
	}
	return true, nil
}

// StoredResult returns the id of the transaction created by an earlier request
// with this key. It fails with ledger.ErrIdempotencyKeyReuse if that earlier
// request was different (its hash does not match).
func (t *Tx) StoredResult(ctx context.Context, key string, requestHash []byte) (uuid.UUID, error) {
	rec, err := t.q.GetIdempotencyKey(ctx, key)
	if err != nil {
		return uuid.Nil, fmt.Errorf("get idempotency key: %w", err)
	}
	if !bytes.Equal(rec.RequestHash, requestHash) {
		return uuid.Nil, ledger.ErrIdempotencyKeyReuse
	}
	if rec.TransactionID == nil {
		return uuid.Nil, errors.New("idempotency key has no recorded result")
	}
	return *rec.TransactionID, nil
}

// CompleteIdempotencyKey records the outcome of the request that claimed the key.
// The stored response is the HTTP status and a small JSON body referencing the
// transaction; replays re-read the (immutable) transaction instead of caching
// a rendered response.
func (t *Tx) CompleteIdempotencyKey(ctx context.Context, key string, status int, transactionID uuid.UUID) error {
	body, err := json.Marshal(map[string]string{"transaction_id": transactionID.String()})
	if err != nil {
		return fmt.Errorf("encode idempotency response: %w", err)
	}
	if err := t.q.CompleteIdempotencyKey(ctx, db.CompleteIdempotencyKeyParams{
		Key: key, ResponseStatus: int32(status), ResponseBody: body, TransactionID: &transactionID, //nolint:gosec // status is a small HTTP code
	}); err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

// DeleteIdempotencyKeysBefore removes keys created before the given instant
// and returns how many were deleted.
func (s *Store) DeleteIdempotencyKeysBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.Queries().DeleteExpiredIdempotencyKeys(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency keys: %w", err)
	}
	return n, nil
}
