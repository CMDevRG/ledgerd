package ledger

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Reversal is a transaction request that offsets an earlier transaction.
type Reversal struct {
	// ReversesID is the transaction being offset; it is stored on the new
	// transaction (transactions.reverses_id) and is unique, which is what
	// prevents a transaction from being reversed twice.
	ReversesID uuid.UUID
	// Request holds the mirror postings: same accounts, amounts and currencies,
	// opposite directions, in the original order.
	Request TransactionRequest
}

// Reverse builds the reversal of tx.
//
// The ledger is append-only, so a mistake is never edited or deleted: it is
// offset by a new transaction that moves the same amounts the other way. The
// result is subject to every normal rule when applied (in particular the
// overdraft check: undoing a deposit that was already spent is rejected).
func Reverse(tx Transaction) (Reversal, error) {
	if tx.ID == uuid.Nil {
		return Reversal{}, errors.New("cannot reverse a transaction without an id")
	}

	postings := make([]PostingInput, len(tx.Postings))
	for i, p := range tx.Postings {
		postings[i] = PostingInput{
			AccountID: p.AccountID,
			Direction: p.Direction.Opposite(),
			Amount:    p.Amount,
			Currency:  p.Currency,
		}
	}
	req := TransactionRequest{
		Description: fmt.Sprintf("Reversal of %s", tx.ID),
		Postings:    postings,
	}
	if err := req.Validate(); err != nil {
		return Reversal{}, fmt.Errorf("transaction %s cannot be reversed: %w", tx.ID, err)
	}
	return Reversal{ReversesID: tx.ID, Request: req}, nil
}
