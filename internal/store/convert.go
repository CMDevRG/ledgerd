package store

import (
	"fmt"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/store/db"
)

func postingToDomain(p db.Posting) (ledger.Posting, error) {
	amount, err := ledger.NewAmount(p.Amount.Big())
	if err != nil {
		// The database CHECK constraint makes this unreachable; failing loudly
		// is better than returning a corrupt value.
		return ledger.Posting{}, fmt.Errorf("posting %d has an invalid amount: %w", p.ID, err)
	}
	return ledger.Posting{
		ID:            p.ID,
		TransactionID: p.TransactionID,
		AccountID:     p.AccountID,
		Direction:     ledger.Side(p.Direction),
		Amount:        amount,
		Currency:      ledger.Currency(p.Currency),
		BalanceAfter:  p.BalanceAfter.Big(),
		RecordedAt:    p.RecordedAt,
	}, nil
}

func postingsToDomain(rows []db.Posting) ([]ledger.Posting, error) {
	out := make([]ledger.Posting, len(rows))
	for i, r := range rows {
		p, err := postingToDomain(r)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

func transactionToDomain(t db.Transaction, postings []db.Posting) (ledger.Transaction, error) {
	ps, err := postingsToDomain(postings)
	if err != nil {
		return ledger.Transaction{}, err
	}
	return ledger.Transaction{
		ID:          t.ID,
		Description: t.Description,
		ReversesID:  t.ReversesID,
		CreatedAt:   t.CreatedAt,
		Postings:    ps,
	}, nil
}
