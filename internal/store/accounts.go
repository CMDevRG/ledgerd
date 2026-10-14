package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/store/db"
)

// accountToDomain converts a database row into the domain type.
func accountToDomain(a db.Account) ledger.Account {
	return ledger.Account{
		ID:            a.ID,
		Code:          a.Code,
		Currency:      ledger.Currency(a.Currency),
		Type:          ledger.AccountType(a.Type),
		NormalBalance: ledger.Side(a.NormalBalance),
		AllowNegative: a.AllowNegative,
		Balance:       a.Balance.Big(),
		LastPostingAt: a.LastPostingAt,
		CreatedAt:     a.CreatedAt,
	}
}

// CreateAccount inserts a new account with a zero balance.
//
// The input is expected to have been validated (ledger.NewAccount.Validate);
// the database constraints are the second line of defence. A duplicate code is
// reported as ledger.ErrAccountCodeExists.
func (s *Store) CreateAccount(ctx context.Context, n ledger.NewAccount) (ledger.Account, error) {
	row, err := s.Queries().CreateAccount(ctx, db.CreateAccountParams{
		Code:          n.Code,
		Currency:      string(n.Currency),
		Type:          string(n.Type),
		NormalBalance: string(n.NormalBalance),
		AllowNegative: n.AllowNegative,
	})
	if err != nil {
		if pgErr := pgError(err); pgErr != nil && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "accounts_code_key" {
			return ledger.Account{}, fmt.Errorf("%w: %q", ledger.ErrAccountCodeExists, n.Code)
		}
		return ledger.Account{}, fmt.Errorf("create account: %w", err)
	}
	return accountToDomain(row), nil
}

// GetAccount returns an account, or an *ledger.AccountNotFoundError.
func (s *Store) GetAccount(ctx context.Context, id uuid.UUID) (ledger.Account, error) {
	row, err := s.Queries().GetAccount(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ledger.Account{}, &ledger.AccountNotFoundError{ID: id}
		}
		return ledger.Account{}, fmt.Errorf("get account: %w", err)
	}
	return accountToDomain(row), nil
}

// LockAccounts takes a row lock (SELECT ... FOR UPDATE) on the given accounts
// and returns their current state. It must be called inside a transaction;
// the locks are released when that transaction ends.
//
// The database locks the rows in ascending id order whatever the order of ids,
// so two transactions that need overlapping sets of accounts can never wait on
// each other in a cycle. If any account does not exist the error is an
// *ledger.AccountNotFoundError for the lowest missing id (deterministic).
func LockAccounts(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID]ledger.Account, error) {
	unique := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		unique[id] = struct{}{}
	}
	wanted := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		wanted = append(wanted, id)
	}
	sort.Slice(wanted, func(i, j int) bool { return bytes.Compare(wanted[i][:], wanted[j][:]) < 0 })

	rows, err := q.LockAccounts(ctx, wanted)
	if err != nil {
		return nil, fmt.Errorf("lock accounts: %w", err)
	}
	locked := make(map[uuid.UUID]ledger.Account, len(rows))
	for _, r := range rows {
		locked[r.ID] = accountToDomain(r)
	}
	for _, id := range wanted {
		if _, ok := locked[id]; !ok {
			return nil, &ledger.AccountNotFoundError{ID: id}
		}
	}
	return locked, nil
}
