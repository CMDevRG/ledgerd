package ledger

import (
	"errors"
	"fmt"
	"strings"
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
