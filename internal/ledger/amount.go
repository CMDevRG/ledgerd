// Package ledger contains the pure domain model of the double-entry ledger:
// money amounts, accounts, postings, transactions and the rules that govern
// them. It has no dependency on HTTP or SQL.
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// MaxAmountDigits is the number of decimal digits of MaxAmount (10^30).
const MaxAmountDigits = 31

// maxAmount is the largest amount accepted in a single posting: 10^30.
// It matches the CHECK constraint on postings.amount in the database.
var maxAmount = new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)

// MaxAmount returns a copy of the largest accepted posting amount (10^30).
func MaxAmount() *big.Int { return new(big.Int).Set(maxAmount) }

// Amount is a strictly positive whole number of minor units (cents, wei, ...).
//
// It is a value object: the zero value is "no amount" and is not valid.
// Amounts are arbitrary precision (math/big) and are never converted through
// floating point. The internal integer is never exposed; Big returns a copy.
type Amount struct {
	v *big.Int
}

// ParseAmount parses the canonical decimal representation of an amount.
//
// The accepted grammar is deliberately narrow so that every amount has exactly
// one textual form (which keeps request hashing for idempotency stable):
//
//	amount = nonzero-digit *digit      ; no sign, no leading zeros, ASCII only
//
// and the value must be between 1 and 10^30 inclusive. Decimal points,
// exponents, whitespace and separators are rejected.
func ParseAmount(s string) (Amount, error) {
	n := len(s)
	if n == 0 {
		return Amount{}, errors.New("amount is empty")
	}
	// Check the length before doing any big-number work, so a huge input
	// cannot be used to burn CPU.
	if n > MaxAmountDigits {
		return Amount{}, fmt.Errorf("amount has more than %d digits", MaxAmountDigits)
	}
	if s[0] < '1' || s[0] > '9' {
		return Amount{}, errors.New("amount must start with a digit from 1 to 9 (no sign, zero, or leading zeros)")
	}
	for i := 1; i < n; i++ {
		if s[i] < '0' || s[i] > '9' {
			return Amount{}, errors.New("amount must contain only decimal digits")
		}
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok { // unreachable after the checks above; defensive
		return Amount{}, errors.New("amount is not a valid integer")
	}
	if v.Cmp(maxAmount) > 0 {
		return Amount{}, errors.New("amount exceeds the maximum of 10^30")
	}
	return Amount{v: v}, nil
}

// NewAmount builds an Amount from a big integer, applying the same range rule
// as ParseAmount (1 <= v <= 10^30). The argument is copied.
func NewAmount(v *big.Int) (Amount, error) {
	if v == nil || v.Sign() <= 0 {
		return Amount{}, errors.New("amount must be positive")
	}
	if v.Cmp(maxAmount) > 0 {
		return Amount{}, errors.New("amount exceeds the maximum of 10^30")
	}
	return Amount{v: new(big.Int).Set(v)}, nil
}

// IsZero reports whether a is the zero value (no amount).
func (a Amount) IsZero() bool { return a.v == nil }

// Big returns a copy of the amount as a big integer. The zero Amount yields 0.
func (a Amount) Big() *big.Int {
	if a.v == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(a.v)
}

// Cmp compares two amounts and returns -1, 0 or +1.
func (a Amount) Cmp(b Amount) int { return a.Big().Cmp(b.Big()) }

// String returns the canonical decimal representation.
func (a Amount) String() string {
	if a.v == nil {
		return "0"
	}
	return a.v.String()
}

// MarshalJSON encodes the amount as a JSON string so that clients which parse
// JSON numbers as floating point cannot lose precision.
func (a Amount) MarshalJSON() ([]byte, error) {
	if a.v == nil {
		return nil, errors.New("cannot marshal the zero Amount")
	}
	return json.Marshal(a.String())
}

// UnmarshalJSON accepts only a JSON string holding a canonical amount.
// JSON numbers and null are rejected on purpose.
func (a *Amount) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("amount must be a JSON string of decimal digits")
	}
	parsed, err := ParseAmount(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}
