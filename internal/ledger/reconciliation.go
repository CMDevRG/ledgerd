package ledger

import (
	"math/big"
	"time"

	"github.com/google/uuid"
)

// BalanceMismatch is an account whose cached balance differs from the balance
// recomputed from its postings.
type BalanceMismatch struct {
	AccountID uuid.UUID
	Code      string
	Currency  Currency
	Stored    *big.Int
	Expected  *big.Int
}

// CurrencyTotals sums all debit and all credit postings of one currency.
type CurrencyTotals struct {
	Currency Currency
	Debits   *big.Int
	Credits  *big.Int
}

// Balanced reports whether debits equal credits, the fundamental invariant of
// double-entry bookkeeping.
func (c CurrencyTotals) Balanced() bool { return c.Debits.Cmp(c.Credits) == 0 }

// ChainBreak is a posting whose balance_after does not follow from the previous
// posting of the same account.
type ChainBreak struct {
	PostingID int64
	AccountID uuid.UUID
	Recorded  *big.Int
	Expected  *big.Int
}

// ReconciliationReport is the result of verifying the ledger against itself.
type ReconciliationReport struct {
	// StartedAt is when the run began (service clock).
	StartedAt time.Time
	// Deep is true if the per-posting balance chain was also verified.
	Deep            bool
	AccountsChecked int64
	Mismatches      []BalanceMismatch
	Currencies      []CurrencyTotals
	// ChainBreaks is only filled when Deep is true.
	ChainBreaks []ChainBreak
}

// OK reports whether the ledger is fully consistent: no balance mismatch, debits
// equal credits in every currency, and (when checked) an unbroken balance chain.
func (r ReconciliationReport) OK() bool {
	if len(r.Mismatches) > 0 || len(r.ChainBreaks) > 0 {
		return false
	}
	for _, c := range r.Currencies {
		if !c.Balanced() {
			return false
		}
	}
	return true
}
