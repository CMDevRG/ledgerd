package service

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"time"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/store"
)

// MinIdempotencyRetention is the shortest time an idempotency key is kept
// (requirement R6.7). It must match config.MinIdempotencyTTL.
const MinIdempotencyRetention = 24 * time.Hour

// Limits of an Idempotency-Key header value.
const (
	maxIdempotencyKeyLength = 255
	requestHashLength       = sha256.Size
)

// Idempotency identifies one logical request so that it can be retried safely.
type Idempotency struct {
	// Key is chosen by the client (the Idempotency-Key header).
	Key string
	// RequestHash fingerprints the request (see RequestHash). The same key with
	// a different hash is rejected.
	RequestHash []byte
}

// RequestHash fingerprints a request for idempotency: SHA-256 over the method,
// the path and the canonical form of the body. Including method and path means
// a key cannot be reused for a different operation, and canonical bytes mean
// that field order and whitespace in the client's JSON do not matter.
func RequestHash(method, path string, canonicalBody []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{'\n'})
	h.Write([]byte(path))
	h.Write([]byte{'\n'})
	h.Write(canonicalBody)
	return h.Sum(nil)
}

func (i Idempotency) validate() error {
	var vs []ledger.Violation
	n := len(i.Key)
	switch {
	case n == 0:
		vs = append(vs, ledger.Violation{Field: "Idempotency-Key", Message: "is required"})
	case n > maxIdempotencyKeyLength:
		vs = append(vs, ledger.Violation{Field: "Idempotency-Key", Message: "must be at most 255 characters"})
	default:
		for j := 0; j < n; j++ {
			if c := i.Key[j]; c < 0x21 || c > 0x7e { // visible ASCII, no spaces
				vs = append(vs, ledger.Violation{Field: "Idempotency-Key", Message: "must contain only visible ASCII characters"})
				break
			}
		}
	}
	if len(i.RequestHash) != requestHashLength {
		vs = append(vs, ledger.Violation{Field: "request_hash", Message: "must be a SHA-256 digest"})
	}
	if len(vs) > 0 {
		return &ledger.ValidationError{Violations: vs}
	}
	return nil
}

// PurgeIdempotencyKeys deletes keys older than retention and returns how many
// were removed. Retention below MinIdempotencyRetention is refused, so a
// misconfiguration cannot shorten the window in which retries are safe.
func (s *Service) PurgeIdempotencyKeys(ctx context.Context, retention time.Duration) (int64, error) {
	if retention < MinIdempotencyRetention {
		return 0, &ledger.ValidationError{Violations: []ledger.Violation{
			{Field: "retention", Message: "must be at least " + MinIdempotencyRetention.String()},
		}}
	}
	return s.store.DeleteIdempotencyKeysBefore(ctx, s.now().Add(-retention))
}

// RunIdempotencyCleanup purges expired keys every interval until ctx is
// cancelled. Errors are logged and do not stop the loop.
func (s *Service) RunIdempotencyCleanup(ctx context.Context, retention, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.PurgeIdempotencyKeys(ctx, retention)
			switch {
			case err != nil && ctx.Err() == nil:
				log.Error("idempotency cleanup failed", "error", err)
			case n > 0:
				log.Info("expired idempotency keys removed", "count", n)
			}
		}
	}
}

// idempotent runs op at most once per key. Inside a single database
// transaction it claims the key, runs op, and records the outcome, so the
// key and the ledger effect commit or roll back together. If the key was
// already completed, the stored transaction is returned instead.
func (s *Service) idempotent(
	ctx context.Context,
	idem Idempotency,
	op func(ctx context.Context, tx *store.Tx) (ledger.Transaction, error),
) (TransactionResult, error) {
	var result TransactionResult
	err := s.store.Do(ctx, func(ctx context.Context, tx *store.Tx) error {
		result = TransactionResult{} // the function can run again after a deadlock retry

		claimed, err := tx.ClaimIdempotencyKey(ctx, idem.Key, idem.RequestHash)
		if err != nil {
			return err
		}
		if !claimed {
			id, err := tx.StoredResult(ctx, idem.Key, idem.RequestHash)
			if err != nil {
				return err
			}
			t, err := tx.LoadTransaction(ctx, id)
			if err != nil {
				return err
			}
			result = TransactionResult{Transaction: t, Replayed: true}
			return nil
		}

		t, err := op(ctx, tx)
		if err != nil {
			return err
		}
		if err := tx.CompleteIdempotencyKey(ctx, idem.Key, createdStatus, t.ID); err != nil {
			return err
		}
		result = TransactionResult{Transaction: t}
		return nil
	})
	if err != nil {
		return TransactionResult{}, err
	}
	return result, nil
}
