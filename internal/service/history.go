package service

import (
	"context"
	"encoding/base64"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
)

// Page sizes for posting history.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Balance is an account's balance at a point in time.
type Balance struct {
	AccountID uuid.UUID
	Currency  ledger.Currency
	// Amount is in minor units; negative only for accounts that allow it.
	Amount *big.Int
	// LastPostingAt is when the posting that produced this balance was
	// recorded; nil if the account had no postings at that time.
	LastPostingAt *time.Time
	// AsOf is the instant that was asked for. It is nil when the current
	// balance is returned (no instant requested, or one in the future).
	AsOf *time.Time
}

// Balance returns an account's current balance, or, when asOf is given, the
// balance it had at that instant: the balance after the latest posting
// recorded at or before asOf (zero before the first posting). An instant in
// the future is answered with the current balance.
func (s *Service) Balance(ctx context.Context, accountID uuid.UUID, asOf *time.Time) (Balance, error) {
	acct, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return Balance{}, err
	}

	if asOf == nil || asOf.After(s.now()) {
		return Balance{
			AccountID:     acct.ID,
			Currency:      acct.Currency,
			Amount:        acct.Balance,
			LastPostingAt: acct.LastPostingAt,
		}, nil
	}

	amount, at, err := s.store.BalanceAsOf(ctx, accountID, *asOf)
	if err != nil {
		return Balance{}, err
	}
	requested := asOf.UTC()
	return Balance{
		AccountID:     acct.ID,
		Currency:      acct.Currency,
		Amount:        amount,
		LastPostingAt: at,
		AsOf:          &requested,
	}, nil
}

// PostingPage is one page of an account's posting history.
type PostingPage struct {
	// Postings are ordered newest first.
	Postings []ledger.Posting
	// NextCursor is empty on the last page; otherwise pass it to ListPostings
	// to get the following (older) page.
	NextCursor string
}

// ListPostings returns an account's postings, newest first, one page at a time.
//
// limit 0 selects DefaultPageSize; otherwise it must be between 1 and
// MaxPageSize. The cursor is opaque: pass the NextCursor of the previous page,
// or "" for the first page. Pages are keyed on the posting id, which only
// grows, so postings added while a client is paging never shift the pages it
// has not read yet: nothing is skipped and nothing is repeated.
func (s *Service) ListPostings(ctx context.Context, accountID uuid.UUID, cursor string, limit int) (PostingPage, error) {
	var vs []ledger.Violation
	if limit == 0 {
		limit = DefaultPageSize
	} else if limit < 0 || limit > MaxPageSize {
		vs = append(vs, ledger.Violation{Field: "limit", Message: "must be between 1 and " + strconv.Itoa(MaxPageSize)})
	}
	var before *int64
	if cursor != "" {
		id, ok := decodeCursor(cursor)
		if !ok {
			vs = append(vs, ledger.Violation{Field: "cursor", Message: "is not a valid cursor"})
		} else {
			before = &id
		}
	}
	if len(vs) > 0 {
		return PostingPage{}, &ledger.ValidationError{Violations: vs}
	}

	if _, err := s.store.GetAccount(ctx, accountID); err != nil {
		return PostingPage{}, err
	}

	// Ask for one extra row: if it comes back, there is another page.
	rows, err := s.store.ListAccountPostings(ctx, accountID, before, limit+1)
	if err != nil {
		return PostingPage{}, err
	}
	page := PostingPage{Postings: rows}
	if len(rows) > limit {
		page.Postings = rows[:limit]
		page.NextCursor = encodeCursor(rows[limit-1].ID)
	}
	return page, nil
}

const cursorPrefix = "v1:"

func encodeCursor(lastID int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(lastID, 10)))
}

func decodeCursor(c string) (int64, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, false
	}
	rest, ok := strings.CutPrefix(string(raw), cursorPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != rest { // canonical form only
		return 0, false
	}
	return id, true
}
