package ledger

import (
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Side is one of the two sides of a double-entry posting. It is used both for
// the direction of a posting and for an account's normal balance.
type Side string

// The two sides of the books.
const (
	Debit  Side = "DEBIT"
	Credit Side = "CREDIT"
)

// Valid reports whether s is DEBIT or CREDIT.
func (s Side) Valid() bool { return s == Debit || s == Credit }

// Opposite returns the other side.
func (s Side) Opposite() Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

// AccountType is the accounting classification of an account.
type AccountType string

// The five account types of double-entry bookkeeping.
const (
	Asset     AccountType = "ASSET"
	Liability AccountType = "LIABILITY"
	Equity    AccountType = "EQUITY"
	Income    AccountType = "INCOME"
	Expense   AccountType = "EXPENSE"
)

// Valid reports whether t is a known account type.
func (t AccountType) Valid() bool {
	switch t {
	case Asset, Liability, Equity, Income, Expense:
		return true
	}
	return false
}

// Currency is an asset code: 2 to 10 uppercase letters or digits
// (USD, EUR, ETH, USDT, ...). The name follows the API field.
type Currency string

var (
	currencyPattern    = regexp.MustCompile(`^[A-Z0-9]{2,10}$`)
	accountCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

// ParseCurrency validates an asset code.
func ParseCurrency(s string) (Currency, error) {
	if !currencyPattern.MatchString(s) {
		return "", fmt.Errorf("must be 2 to 10 uppercase letters or digits, got %q", s)
	}
	return Currency(s), nil
}

// Limits of a transaction request.
const (
	MinPostings          = 2
	MaxPostings          = 100
	MaxDescriptionLength = 500 // characters (runes)
)

// Account is a ledger account. Balance is the cached running total, kept equal
// to the sum of the account's postings (see Reconciliation).
type Account struct {
	ID            uuid.UUID
	Code          string
	Currency      Currency
	Type          AccountType
	NormalBalance Side
	AllowNegative bool
	Balance       *big.Int
	LastPostingAt *time.Time
	CreatedAt     time.Time
}

// NewAccount is the input for creating an account.
type NewAccount struct {
	Code          string
	Currency      Currency
	Type          AccountType
	NormalBalance Side
	AllowNegative bool
}

// Validate checks every field and reports all violations at once.
func (n NewAccount) Validate() error {
	var vs violations
	if !accountCodePattern.MatchString(n.Code) {
		vs.add("code", "must be 1 to 64 characters: letters, digits, '.', '_', ':' or '-', starting with a letter or digit")
	}
	if _, err := ParseCurrency(string(n.Currency)); err != nil {
		vs.add("currency", "%s", err.Error())
	}
	if !n.Type.Valid() {
		vs.add("type", "must be one of ASSET, LIABILITY, EQUITY, INCOME, EXPENSE")
	}
	if !n.NormalBalance.Valid() {
		vs.add("normal_balance", "must be DEBIT or CREDIT")
	}
	return vs.err()
}

// PostingInput is one line of a transaction request.
type PostingInput struct {
	AccountID uuid.UUID
	Direction Side
	Amount    Amount
	Currency  Currency
}

// TransactionRequest is the input for recording a transaction.
type TransactionRequest struct {
	Description string
	Postings    []PostingInput
}

// Posting is a persisted line of a transaction: one debit or credit on one
// account, with the account balance that resulted from it.
type Posting struct {
	ID            int64
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Direction     Side
	Amount        Amount
	Currency      Currency
	BalanceAfter  *big.Int
	RecordedAt    time.Time
}

// Transaction is an atomic, balanced group of postings.
type Transaction struct {
	ID          uuid.UUID
	Description string
	ReversesID  *uuid.UUID
	CreatedAt   time.Time
	Postings    []Posting
}
