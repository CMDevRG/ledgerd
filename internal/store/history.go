package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/store/db"
)

// BalanceAsOf returns an account's balance right after its latest posting
// recorded at or before asOf, and that posting's timestamp. If there is no such
// posting the balance is zero and the timestamp is nil.
//
// asOf is truncated to microseconds, the resolution of stored timestamps, so
// that rounding by the database can never include a posting that happened
// after the requested instant.
func (s *Store) BalanceAsOf(ctx context.Context, accountID uuid.UUID, asOf time.Time) (*big.Int, *time.Time, error) {
	row, err := s.Queries().GetBalanceAsOf(ctx, db.GetBalanceAsOfParams{
		AccountID: accountID,
		AsOf:      asOf.UTC().Truncate(time.Microsecond),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("balance as of: %w", err)
	}
	at := row.RecordedAt
	return row.BalanceAfter.Big(), &at, nil
}

// ListAccountPostings returns up to limit postings of an account, newest first.
// If beforeID is not nil, only postings with a smaller id are returned, which
// is how the next page is requested.
func (s *Store) ListAccountPostings(ctx context.Context, accountID uuid.UUID, beforeID *int64, limit int) ([]ledger.Posting, error) {
	cursor := pgtype.Int8{}
	if beforeID != nil {
		cursor = pgtype.Int8{Int64: *beforeID, Valid: true}
	}
	rows, err := s.Queries().ListAccountPostings(ctx, db.ListAccountPostingsParams{
		AccountID: accountID,
		BeforeID:  cursor,
		PageSize:  int32(limit), //nolint:gosec // limit is validated to be at most 201 by the caller
	})
	if err != nil {
		return nil, fmt.Errorf("list account postings: %w", err)
	}
	return postingsToDomain(rows)
}
