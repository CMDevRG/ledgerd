package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/store/db"
)

// Snapshot gives read-only access to one consistent view of the database: every
// query made through it sees exactly the same committed data, however many
// writes happen meanwhile.
type Snapshot struct {
	q  *db.Queries
	tx pgx.Tx
}

// ReadSnapshot runs fn inside a REPEATABLE READ, READ ONLY transaction.
//
// Under REPEATABLE READ the transaction reads from the snapshot taken by its
// first query, so results are mutually consistent; READ ONLY makes the
// database itself refuse any write. It takes no row locks, so concurrent
// transactions are never blocked by it.
func (s *Store) ReadSnapshot(ctx context.Context, fn func(ctx context.Context, snap *Snapshot) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	return s.retrying(ctx, func() error {
		return s.runOnce(ctx, opts, func(ctx context.Context, tx pgx.Tx) error {
			return fn(ctx, &Snapshot{q: db.New(tx), tx: tx})
		})
	})
}

// Settings reports the isolation level and read-only flag of the transaction.
// It exists for diagnostics and tests.
func (sn *Snapshot) Settings(ctx context.Context) (isolation string, readOnly bool, err error) {
	row := sn.tx.QueryRow(ctx, `SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only') = 'on'`)
	if err := row.Scan(&isolation, &readOnly); err != nil {
		return "", false, fmt.Errorf("read transaction settings: %w", err)
	}
	return isolation, readOnly, nil
}

// CountAccounts returns the number of accounts.
func (sn *Snapshot) CountAccounts(ctx context.Context) (int64, error) {
	n, err := sn.q.CountAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return n, nil
}

// BalanceMismatches returns the accounts whose stored balance differs from the
// sum of their postings, ordered by id.
func (sn *Snapshot) BalanceMismatches(ctx context.Context) ([]ledger.BalanceMismatch, error) {
	rows, err := sn.q.FindBalanceMismatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("find balance mismatches: %w", err)
	}
	out := make([]ledger.BalanceMismatch, len(rows))
	for i, r := range rows {
		out[i] = ledger.BalanceMismatch{
			AccountID: r.ID, Code: r.Code, Currency: ledger.Currency(r.Currency),
			Stored: r.Stored.Big(), Expected: r.Expected.Big(),
		}
	}
	return out, nil
}

// CurrencyTotals returns total debits and credits per currency, ordered by currency.
func (sn *Snapshot) CurrencyTotals(ctx context.Context) ([]ledger.CurrencyTotals, error) {
	rows, err := sn.q.LedgerTotalsByCurrency(ctx)
	if err != nil {
		return nil, fmt.Errorf("sum postings by currency: %w", err)
	}
	out := make([]ledger.CurrencyTotals, len(rows))
	for i, r := range rows {
		out[i] = ledger.CurrencyTotals{Currency: ledger.Currency(r.Currency), Debits: r.Debits.Big(), Credits: r.Credits.Big()}
	}
	return out, nil
}

// BalanceChainBreaks returns the postings whose balance_after is inconsistent
// with the previous posting of the same account, ordered by posting id.
func (sn *Snapshot) BalanceChainBreaks(ctx context.Context) ([]ledger.ChainBreak, error) {
	rows, err := sn.q.FindBalanceChainBreaks(ctx)
	if err != nil {
		return nil, fmt.Errorf("find balance chain breaks: %w", err)
	}
	out := make([]ledger.ChainBreak, len(rows))
	for i, r := range rows {
		out[i] = ledger.ChainBreak{PostingID: r.ID, AccountID: r.AccountID, Recorded: r.BalanceAfter.Big(), Expected: r.Expected.Big()}
	}
	return out, nil
}
