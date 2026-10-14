package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
)

func TestErrorToProblem(t *testing.T) {
	acct := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		unexpected bool
	}{
		{"validation", &ledger.ValidationError{Violations: []ledger.Violation{{Field: "code", Message: "bad"}}}, 422, CodeValidationError, false},
		{"wrapped validation", fmt.Errorf("outer: %w", &ledger.ValidationError{}), 422, CodeValidationError, false},
		{"unbalanced", &ledger.UnbalancedError{Currency: "USD", Debits: "5", Credits: "4"}, 422, CodeUnbalanced, false},
		{"insufficient funds", &ledger.InsufficientFundsError{AccountID: acct, Shortfall: "30"}, 422, CodeInsufficientFunds, false},
		{"key reuse", ledger.ErrIdempotencyKeyReuse, 422, CodeIdempotencyKeyReuse, false},
		{"account not found", &ledger.AccountNotFoundError{ID: acct}, 404, CodeNotFound, false},
		{"transaction not found", &ledger.TransactionNotFoundError{ID: acct}, 404, CodeNotFound, false},
		{"code exists", fmt.Errorf("%w: %q", ledger.ErrAccountCodeExists, "x"), 409, CodeAccountCodeExists, false},
		{"already reversed", ledger.ErrAlreadyReversed, 409, CodeAlreadyReversed, false},
		{"unavailable", fmt.Errorf("%w: gave up", ledger.ErrTemporarilyUnavailable), 503, CodeTemporarilyUnavailble, false},
		{"deadline", context.DeadlineExceeded, 503, CodeTemporarilyUnavailble, false},
		{"cancelled", context.Canceled, 503, CodeTemporarilyUnavailble, false},
		{"request error", &requestError{CodeMalformedRequest, "bad json"}, 400, CodeMalformedRequest, false},
		{"too large", &requestError{CodePayloadTooLarge, "big"}, 413, CodePayloadTooLarge, false},
		{"media type", &requestError{CodeUnsupportedMediaType, "ct"}, 415, CodeUnsupportedMediaType, false},
		{"unexpected", errors.New("pq: relation \"accounts\" does not exist at /srv/app/store.go:42"), 500, CodeInternalError, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, unexpected := errorToProblem(tc.err)
			if p.Status != tc.status || p.Code != tc.code || unexpected != tc.unexpected {
				t.Errorf("got status=%d code=%s unexpected=%v; want %d %s %v", p.Status, p.Code, unexpected, tc.status, tc.code, tc.unexpected)
			}
			if p.Type != problemTypePrefix+tc.code || p.Title == "" {
				t.Errorf("type/title missing: %+v", p)
			}
		})
	}
}

func TestErrorToProblem_NeverLeaksInternalDetails(t *testing.T) {
	secretErr := errors.New(`ERROR: relation "accounts" does not exist (SQLSTATE 42P01) /srv/app/store.go:42 password=hunter2`)
	for _, err := range []error{secretErr, fmt.Errorf("record transaction: %w", secretErr)} {
		p, unexpected := errorToProblem(err)
		if !unexpected || p.Code != CodeInternalError {
			t.Fatalf("an unknown error must be a generic internal error: %+v", p)
		}
		text := p.Detail + p.Title
		for _, leak := range []string{"relation", "accounts", "SQLSTATE", "store.go", "hunter2"} {
			if strings.Contains(text, leak) {
				t.Errorf("internal detail %q leaked into the problem: %+v", leak, p)
			}
		}
	}
}

func TestErrorToProblem_ValidationCarriesEveryFieldError(t *testing.T) {
	p, _ := errorToProblem(&ledger.ValidationError{Violations: []ledger.Violation{
		{Field: "postings[0].amount", Message: "is required"},
		{Field: "description", Message: "too long"},
	}})
	if len(p.Errors) != 2 || p.Errors[0].Field != "postings[0].amount" || p.Errors[1].Message != "too long" {
		t.Errorf("errors = %+v", p.Errors)
	}
}

func TestErrorToProblem_DomainDetailsAreActionable(t *testing.T) {
	acct := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	p, _ := errorToProblem(&ledger.InsufficientFundsError{AccountID: acct, Shortfall: "30"})
	if !strings.Contains(p.Detail, acct.String()) || !strings.Contains(p.Detail, "30") {
		t.Errorf("detail should name the account and the shortfall: %q", p.Detail)
	}
	p, _ = errorToProblem(&ledger.UnbalancedError{Currency: "ETH", Debits: "7", Credits: "6"})
	if !strings.Contains(p.Detail, "ETH") || !strings.Contains(p.Detail, "7") || !strings.Contains(p.Detail, "6") {
		t.Errorf("detail should state the currency and both totals: %q", p.Detail)
	}
}

func TestNewProblem_UnknownCodeFailsSafe(t *testing.T) {
	if p := newProblem("not-in-catalog", "x"); p.Status != 500 || p.Code != CodeInternalError {
		t.Errorf("got %+v", p)
	}
}

func TestEveryCatalogCodeIsConsistent(t *testing.T) {
	for code, entry := range catalog {
		p := newProblem(code, "d")
		if p.Status != entry.status || p.Code != code || !strings.HasPrefix(p.Type, "urn:ledgerd:error:") {
			t.Errorf("%s: %+v", code, p)
		}
	}
}
