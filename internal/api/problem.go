// Package api is the HTTP layer: routing, authentication, request/response
// encoding and the mapping of domain errors to RFC 9457 problem responses.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/radadev/ledgerd/internal/ledger"
)

const (
	problemContentType = "application/problem+json"
	problemTypePrefix  = "urn:ledgerd:error:"
)

// FieldError locates one validation problem.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Problem is an RFC 9457 "problem details" document with two extensions: a
// stable machine-readable `code` and the id of the request.
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
}

// Error codes. They are part of the API contract.
const (
	CodeMalformedRequest      = "malformed_request"
	CodeUnauthorized          = "unauthorized"
	CodeNotFound              = "not_found"
	CodeMethodNotAllowed      = "method_not_allowed"
	CodeAccountCodeExists     = "account_code_exists"
	CodeAlreadyReversed       = "already_reversed"
	CodePayloadTooLarge       = "payload_too_large"
	CodeUnsupportedMediaType  = "unsupported_media_type"
	CodeValidationError       = "validation_error"
	CodeUnbalanced            = "unbalanced_transaction"
	CodeInsufficientFunds     = "insufficient_funds"
	CodeIdempotencyKeyReuse   = "idempotency_key_reuse"
	CodeTemporarilyUnavailble = "temporarily_unavailable"
	CodeInternalError         = "internal_error"
	CodeNotReady              = "not_ready"
)

var catalog = map[string]struct {
	status int
	title  string
}{
	CodeMalformedRequest:      {http.StatusBadRequest, "Malformed request"},
	CodeUnauthorized:          {http.StatusUnauthorized, "Unauthorized"},
	CodeNotFound:              {http.StatusNotFound, "Not found"},
	CodeMethodNotAllowed:      {http.StatusMethodNotAllowed, "Method not allowed"},
	CodeAccountCodeExists:     {http.StatusConflict, "Account code already exists"},
	CodeAlreadyReversed:       {http.StatusConflict, "Transaction already reversed"},
	CodePayloadTooLarge:       {http.StatusRequestEntityTooLarge, "Request body too large"},
	CodeUnsupportedMediaType:  {http.StatusUnsupportedMediaType, "Unsupported media type"},
	CodeValidationError:       {http.StatusUnprocessableEntity, "Validation failed"},
	CodeUnbalanced:            {http.StatusUnprocessableEntity, "Transaction is not balanced"},
	CodeInsufficientFunds:     {http.StatusUnprocessableEntity, "Insufficient funds"},
	CodeIdempotencyKeyReuse:   {http.StatusUnprocessableEntity, "Idempotency key reused with a different request"},
	CodeTemporarilyUnavailble: {http.StatusServiceUnavailable, "Temporarily unavailable"},
	CodeInternalError:         {http.StatusInternalServerError, "Internal error"},
	CodeNotReady:              {http.StatusServiceUnavailable, "Service not ready"},
}

// newProblem builds a problem from the catalog.
func newProblem(code, detail string) Problem {
	c, ok := catalog[code]
	if !ok { // programming error; fail safe
		c = catalog[CodeInternalError]
		code = CodeInternalError
	}
	return Problem{Type: problemTypePrefix + code, Title: c.title, Status: c.status, Code: code, Detail: detail}
}

// requestError is a problem with the way the request itself was made (bad JSON,
// wrong content type, body too large), as opposed to a domain error.
type requestError struct {
	code   string
	detail string
}

func (e *requestError) Error() string { return e.code + ": " + e.detail }

// errorToProblem translates any error into the problem to send. Unknown errors
// become a generic internal error: their text may contain SQL or paths and must
// never reach the client. The second result tells the caller whether the error
// was unexpected (and so must be logged).
func errorToProblem(err error) (p Problem, unexpected bool) {
	var (
		re  *requestError
		ve  *ledger.ValidationError
		ub  *ledger.UnbalancedError
		ife *ledger.InsufficientFundsError
	)
	switch {
	case errors.As(err, &re):
		return newProblem(re.code, re.detail), false
	case errors.As(err, &ve):
		p = newProblem(CodeValidationError, "one or more fields are invalid")
		for _, v := range ve.Violations {
			p.Errors = append(p.Errors, FieldError{Field: v.Field, Message: v.Message})
		}
		return p, false
	case errors.As(err, &ub):
		return newProblem(CodeUnbalanced, fmt.Sprintf("for %s, total debits (%s) differ from total credits (%s)", ub.Currency, ub.Debits, ub.Credits)), false
	case errors.As(err, &ife):
		return newProblem(CodeInsufficientFunds, fmt.Sprintf("account %s would be overdrawn by %s", ife.AccountID, ife.Shortfall)), false
	case errors.Is(err, ledger.ErrIdempotencyKeyReuse):
		return newProblem(CodeIdempotencyKeyReuse, "this Idempotency-Key was already used with a different request"), false
	case errors.Is(err, ledger.ErrNotFound):
		return newProblem(CodeNotFound, err.Error()), false
	case errors.Is(err, ledger.ErrAccountCodeExists):
		return newProblem(CodeAccountCodeExists, "an account with this code already exists"), false
	case errors.Is(err, ledger.ErrAlreadyReversed):
		return newProblem(CodeAlreadyReversed, "this transaction has already been reversed"), false
	case errors.Is(err, ledger.ErrTemporarilyUnavailable),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		// A timed-out or abandoned request changed nothing the client can see;
		// it may simply try again.
		return newProblem(CodeTemporarilyUnavailble, "the service is busy; retry shortly"), false
	default:
		return newProblem(CodeInternalError, "an unexpected error occurred"), true
	}
}

// writeProblem sends p as application/problem+json.
func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.Instance = r.URL.Path
	p.RequestID = requestIDFrom(r.Context())
	if p.RequestID == "" {
		p.RequestID = w.Header().Get(requestIDHeader)
	}

	body, err := json.Marshal(p)
	if err != nil { // cannot happen for this struct; keep the contract anyway
		http.Error(w, `{"status":500}`, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", problemContentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if p.Status == http.StatusServiceUnavailable {
		h.Set("Retry-After", "1")
	}
	if p.Status == http.StatusUnauthorized {
		h.Set("WWW-Authenticate", `Bearer realm="ledgerd"`)
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}
