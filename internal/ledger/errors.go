package ledger

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Violation describes one invalid field in a request.
type Violation struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError reports every rule that a request breaks, so that a client
// can fix all problems in one round trip instead of one at a time.
type ValidationError struct {
	Violations []Violation
}

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		parts[i] = v.Field + ": " + v.Message
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// violations collects violations while validating.
type violations []Violation

func (vs *violations) add(field, format string, args ...any) {
	*vs = append(*vs, Violation{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (vs violations) err() error {
	if len(vs) == 0 {
		return nil
	}
	return &ValidationError{Violations: vs}
}

// ErrUnbalanced is matched (errors.Is) by every *UnbalancedError.
var ErrUnbalanced = errors.New("transaction is not balanced")

// UnbalancedError reports that, for one currency, total debits differ from
// total credits.
type UnbalancedError struct {
	Currency Currency
	Debits   string
	Credits  string
}

// Error implements error.
func (e *UnbalancedError) Error() string {
	return fmt.Sprintf("transaction is not balanced for %s: debits %s != credits %s", e.Currency, e.Debits, e.Credits)
}

// Is makes errors.Is(err, ErrUnbalanced) true for any *UnbalancedError.
func (e *UnbalancedError) Is(target error) bool { return target == ErrUnbalanced }

// ErrInsufficientFunds is matched (errors.Is) by every *InsufficientFundsError.
var ErrInsufficientFunds = errors.New("insufficient funds")

// InsufficientFundsError reports that applying a transaction would leave an
// account that must not go negative with a negative balance.
type InsufficientFundsError struct {
	AccountID uuid.UUID
	// Balance is the balance the account would have had (negative).
	Balance string
	// Shortfall is how much is missing to keep the balance at zero.
	Shortfall string
}

// Error implements error.
func (e *InsufficientFundsError) Error() string {
	return fmt.Sprintf("insufficient funds: account %s would be overdrawn by %s", e.AccountID, e.Shortfall)
}

// Is makes errors.Is(err, ErrInsufficientFunds) true.
func (e *InsufficientFundsError) Is(target error) bool { return target == ErrInsufficientFunds }

// ErrNotFound is matched (errors.Is) by every "not found" domain error.
var ErrNotFound = errors.New("not found")

// AccountNotFoundError reports a posting that references an unknown account.
type AccountNotFoundError struct {
	ID uuid.UUID
}

// Error implements error.
func (e *AccountNotFoundError) Error() string { return fmt.Sprintf("account %s not found", e.ID) }

// Is makes errors.Is(err, ErrNotFound) true.
func (e *AccountNotFoundError) Is(target error) bool { return target == ErrNotFound }

// ErrAccountCodeExists is returned when creating an account whose code is
// already in use.
var ErrAccountCodeExists = errors.New("account code already exists")

// ErrTemporarilyUnavailable means the operation was not performed because of
// contention (lock timeout, repeated deadlock or serialization failure). Nothing
// was changed and the caller may retry.
var ErrTemporarilyUnavailable = errors.New("temporarily unavailable, retry later")

// ErrAlreadyReversed is returned when reversing a transaction that already has a reversal.
var ErrAlreadyReversed = errors.New("transaction has already been reversed")

// TransactionNotFoundError reports an unknown transaction id.
type TransactionNotFoundError struct {
	ID uuid.UUID
}

// Error implements error.
func (e *TransactionNotFoundError) Error() string {
	return fmt.Sprintf("transaction %s not found", e.ID)
}

// Is makes errors.Is(err, ErrNotFound) true.
func (e *TransactionNotFoundError) Is(target error) bool { return target == ErrNotFound }
