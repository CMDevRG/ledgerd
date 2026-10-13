package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
)

// Ledger is what the HTTP layer needs from the service layer. *service.Service
// satisfies it; tests use a fake.
type Ledger interface {
	CreateAccount(ctx context.Context, n ledger.NewAccount) (ledger.Account, error)
	GetAccount(ctx context.Context, id uuid.UUID) (ledger.Account, error)
	Balance(ctx context.Context, id uuid.UUID, asOf *time.Time) (service.Balance, error)
	ListPostings(ctx context.Context, id uuid.UUID, cursor string, limit int) (service.PostingPage, error)
	CreateTransaction(ctx context.Context, idem service.Idempotency, req ledger.TransactionRequest) (service.TransactionResult, error)
	GetTransaction(ctx context.Context, id uuid.UUID) (ledger.Transaction, error)
	Reverse(ctx context.Context, idem service.Idempotency, id uuid.UUID) (service.TransactionResult, error)
}

// Reconciler runs a reconciliation of the whole ledger.
type Reconciler func(ctx context.Context, deep bool) (ledger.ReconciliationReport, error)

// Deps are the collaborators of the HTTP handlers.
type Deps struct {
	Ledger    Ledger
	Reconcile Reconciler
	Ready     func(ctx context.Context) error // nil means "always ready"
}

const idempotencyKeyHeader = "Idempotency-Key"

type handlers struct {
	d Deps
}

// New returns the complete HTTP handler of the service.
func New(o Options, d Deps) http.Handler {
	h := &handlers{d: d}
	return build(o, func(add routeAdder) {
		add("POST /v1/accounts", h.createAccount)
		add("GET /v1/accounts/{id}", h.getAccount)
		add("GET /v1/accounts/{id}/balance", h.getBalance)
		add("GET /v1/accounts/{id}/postings", h.listPostings)
		add("POST /v1/transactions", h.createTransaction)
		add("GET /v1/transactions/{id}", h.getTransaction)
		add("POST /v1/transactions/{id}/reverse", h.reverseTransaction)
		add("POST /v1/reconciliations", h.reconcile)
	})
}

// fail writes the problem for err and returns. Unexpected errors are logged
// by the access log through the 5xx status; their text stays out of responses.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	p, _ := errorToProblem(err)
	writeProblem(w, r, p)
}

// ---- accounts ----

func (h *handlers) createAccount(w http.ResponseWriter, r *http.Request) {
	var body accountRequest
	if err := decodeJSON(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	acct, err := h.d.Ledger.CreateAccount(r.Context(), body.toDomain())
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/accounts/"+acct.ID.String())
	writeJSON(w, r, http.StatusCreated, accountDTO(acct))
}

func (h *handlers) getAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	acct, err := h.d.Ledger.GetAccount(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, accountDTO(acct))
}

func (h *handlers) getBalance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q, err := query(r, "as_of")
	if err != nil {
		fail(w, r, err)
		return
	}
	var asOf *time.Time
	if q.has("as_of") {
		t, err := time.Parse(time.RFC3339Nano, q.get("as_of"))
		if err != nil {
			fail(w, r, fieldError("as_of", "must be an RFC 3339 timestamp, for example 2026-10-13T15:04:05Z"))
			return
		}
		asOf = &t
	}
	b, err := h.d.Ledger.Balance(r.Context(), id, asOf)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, balanceDTO(b))
}

func (h *handlers) listPostings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q, err := query(r, "cursor", "limit")
	if err != nil {
		fail(w, r, err)
		return
	}
	limit := 0 // 0 selects the default page size
	if q.has("limit") {
		n, err := strconv.Atoi(q.get("limit"))
		if err != nil || n < 1 || n > service.MaxPageSize {
			fail(w, r, fieldError("limit", "must be an integer between 1 and "+strconv.Itoa(service.MaxPageSize)))
			return
		}
		limit = n
	}
	if q.has("cursor") && q.get("cursor") == "" {
		fail(w, r, fieldError("cursor", "must not be empty"))
		return
	}
	page, err := h.d.Ledger.ListPostings(r.Context(), id, q.get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, pageDTO(page))
}

// ---- transactions ----

func (h *handlers) createTransaction(w http.ResponseWriter, r *http.Request) {
	var body transactionRequest
	if err := decodeJSON(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	req, canonical, err := body.toDomain()
	if err != nil {
		fail(w, r, err)
		return
	}
	idem, err := idempotency(r, http.MethodPost, "/v1/transactions", canonical)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.d.Ledger.CreateTransaction(r.Context(), idem, req)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeTransaction(w, r, res)
}

func (h *handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	t, err := h.d.Ledger.GetTransaction(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, transactionDTO(t))
}

func (h *handlers) reverseTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The operation has no parameters. A body is tolerated but must respect the
	// size limit, and is drained so the connection can be reused.
	if err := discardBody(r); err != nil {
		fail(w, r, err)
		return
	}
	idem, err := idempotency(r, http.MethodPost, "/v1/transactions/"+id.String()+"/reverse", nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.d.Ledger.Reverse(r.Context(), idem, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeTransaction(w, r, res)
}

// writeTransaction answers a write: 201 with the transaction, flagged when it
// is a replay of an earlier identical request (same status, as the original).
func writeTransaction(w http.ResponseWriter, r *http.Request, res service.TransactionResult) {
	w.Header().Set("Location", "/v1/transactions/"+res.Transaction.ID.String())
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, r, http.StatusCreated, transactionDTO(res.Transaction))
}

// ---- reconciliation ----

func (h *handlers) reconcile(w http.ResponseWriter, r *http.Request) {
	q, err := query(r, "deep")
	if err != nil {
		fail(w, r, err)
		return
	}
	deep := false
	if q.has("deep") {
		switch q.get("deep") {
		case "true":
			deep = true
		case "false":
		default:
			fail(w, r, fieldError("deep", "must be true or false"))
			return
		}
	}
	if err := discardBody(r); err != nil {
		fail(w, r, err)
		return
	}
	report, err := h.d.Reconcile(r.Context(), deep)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The HTTP status says the check ran; whether the ledger is consistent is in
	// the body ("ok"), so a monitoring client can alert on it.
	writeJSON(w, r, http.StatusOK, reconciliationDTO(report))
}

// ---- request helpers ----

func pathID(r *http.Request) (uuid.UUID, error) {
	id, ok := parseUUID(r.PathValue("id"))
	if !ok {
		return uuid.Nil, &requestError{CodeMalformedRequest, "the id in the path must be a UUID"}
	}
	return id, nil
}

func fieldError(field, message string) error {
	return &ledger.ValidationError{Violations: []ledger.Violation{{Field: field, Message: message}}}
}

// idempotency builds the idempotency identity of a write from its header and
// the canonical form of the request.
func idempotency(r *http.Request, method, path string, canonicalBody []byte) (service.Idempotency, error) {
	values := r.Header.Values(idempotencyKeyHeader)
	if len(values) > 1 {
		return service.Idempotency{}, fieldError(idempotencyKeyHeader, "must be provided exactly once")
	}
	key := ""
	if len(values) == 1 {
		key = values[0]
	}
	return service.Idempotency{Key: key, RequestHash: service.RequestHash(method, path, canonicalBody)}, nil
}

// queryParams gives strict access to the query string: unknown or repeated
// parameters are rejected rather than ignored, because a mistyped `as_of` that
// silently returns the current balance would be a dangerous kind of success.
type queryParams struct{ v url.Values }

func (q queryParams) has(k string) bool   { _, ok := q.v[k]; return ok }
func (q queryParams) get(k string) string { return q.v.Get(k) }

func query(r *http.Request, allowed ...string) (queryParams, error) {
	v := r.URL.Query()
	for k, vals := range v {
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			return queryParams{}, &requestError{CodeMalformedRequest, "unknown query parameter " + strconv.Quote(k)}
		}
		if len(vals) != 1 {
			return queryParams{}, &requestError{CodeMalformedRequest, "query parameter " + strconv.Quote(k) + " must be given once"}
		}
	}
	return queryParams{v: v}, nil
}
