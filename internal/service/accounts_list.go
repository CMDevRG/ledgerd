package service

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/store"
)

// ListAccountsParams selects a page of accounts.
type ListAccountsParams struct {
	// CodePrefix and Currency are optional filters; nil means "not given".
	CodePrefix *string
	Currency   *string
	// Cursor is the NextCursor of the previous page, or "" for the first page.
	Cursor string
	// Limit 0 selects DefaultPageSize; otherwise 1 to MaxPageSize.
	Limit int
}

// AccountPage is one page of accounts.
type AccountPage struct {
	// Accounts are newest first (creation time, then id).
	Accounts []ledger.Account
	// NextCursor is empty on the last page.
	NextCursor string
}

// ListAccounts returns accounts newest first, one page at a time.
//
// Pages are keyed on (created_at, id), which never changes for an account, so
// accounts created while a client pages cannot make it skip or repeat one that
// existed when it started.
func (s *Service) ListAccounts(ctx context.Context, p ListAccountsParams) (AccountPage, error) {
	var vs []ledger.Violation
	limit := p.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		vs = append(vs, ledger.Violation{Field: "limit", Message: "must be between 1 and " + strconv.Itoa(MaxPageSize)})
	}

	var filter store.AccountFilter
	if p.CodePrefix != nil {
		if !ledger.ValidAccountCode(*p.CodePrefix) {
			vs = append(vs, ledger.Violation{Field: "code_prefix", Message: "must be 1 to 64 characters of letters, digits, '.', '_', ':' or '-', starting with a letter or digit"})
		}
		filter.CodePrefix = *p.CodePrefix
	}
	if p.Currency != nil {
		if _, err := ledger.ParseCurrency(*p.Currency); err != nil {
			vs = append(vs, ledger.Violation{Field: "currency", Message: "must be 2 to 10 uppercase letters or digits"})
		}
		filter.Currency = *p.Currency
	}

	var after *store.AccountCursor
	if p.Cursor != "" {
		c, ok := decodeAccountCursor(p.Cursor)
		if !ok {
			vs = append(vs, ledger.Violation{Field: "cursor", Message: "is not a valid cursor"})
		}
		after = &c
	}
	if len(vs) > 0 {
		return AccountPage{}, &ledger.ValidationError{Violations: vs}
	}

	// One extra row tells us whether another page exists.
	rows, err := s.store.ListAccounts(ctx, filter, after, limit+1)
	if err != nil {
		return AccountPage{}, err
	}
	page := AccountPage{Accounts: rows}
	if len(rows) > limit {
		page.Accounts = rows[:limit]
		last := rows[limit-1]
		page.NextCursor = encodeAccountCursor(store.AccountCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

const accountCursorPrefix = "v1:"

func encodeAccountCursor(c store.AccountCursor) string {
	raw := accountCursorPrefix + strconv.FormatInt(c.CreatedAt.UnixMicro(), 10) + ":" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeAccountCursor accepts only the exact form encodeAccountCursor produces.
func decodeAccountCursor(s string) (store.AccountCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return store.AccountCursor{}, false
	}
	rest, ok := strings.CutPrefix(string(raw), accountCursorPrefix)
	if !ok {
		return store.AccountCursor{}, false
	}
	microsText, idText, ok := strings.Cut(rest, ":")
	if !ok {
		return store.AccountCursor{}, false
	}
	micros, err := strconv.ParseInt(microsText, 10, 64)
	if err != nil || micros <= 0 || strconv.FormatInt(micros, 10) != microsText {
		return store.AccountCursor{}, false
	}
	id, err := uuid.Parse(idText)
	if err != nil || id.String() != idText || len(idText) != 36 {
		return store.AccountCursor{}, false
	}
	return store.AccountCursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: id}, true
}
