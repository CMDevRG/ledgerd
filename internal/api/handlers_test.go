package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
)

var (
	idA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	idB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	idT = uuid.MustParse("11111111-1111-1111-1111-111111111111")
)

var errNotWired = errors.New("fake ledger: method not wired in this test")

// fakeLedger lets each test supply only the behaviour it needs.
type fakeLedger struct {
	createAccount     func(ledger.NewAccount) (ledger.Account, error)
	getAccount        func(uuid.UUID) (ledger.Account, error)
	balance           func(uuid.UUID, *time.Time) (service.Balance, error)
	listPostings      func(uuid.UUID, string, int) (service.PostingPage, error)
	createTransaction func(service.Idempotency, ledger.TransactionRequest) (service.TransactionResult, error)
	getTransaction    func(uuid.UUID) (ledger.Transaction, error)
	reverse           func(service.Idempotency, uuid.UUID) (service.TransactionResult, error)
}

func (f *fakeLedger) CreateAccount(_ context.Context, n ledger.NewAccount) (ledger.Account, error) {
	if f.createAccount == nil {
		return ledger.Account{}, errNotWired
	}
	return f.createAccount(n)
}
func (f *fakeLedger) GetAccount(_ context.Context, id uuid.UUID) (ledger.Account, error) {
	if f.getAccount == nil {
		return ledger.Account{}, errNotWired
	}
	return f.getAccount(id)
}
func (f *fakeLedger) Balance(_ context.Context, id uuid.UUID, asOf *time.Time) (service.Balance, error) {
	if f.balance == nil {
		return service.Balance{}, errNotWired
	}
	return f.balance(id, asOf)
}
func (f *fakeLedger) ListPostings(_ context.Context, id uuid.UUID, cursor string, limit int) (service.PostingPage, error) {
	if f.listPostings == nil {
		return service.PostingPage{}, errNotWired
	}
	return f.listPostings(id, cursor, limit)
}
func (f *fakeLedger) CreateTransaction(_ context.Context, i service.Idempotency, r ledger.TransactionRequest) (service.TransactionResult, error) {
	if f.createTransaction == nil {
		return service.TransactionResult{}, errNotWired
	}
	return f.createTransaction(i, r)
}
func (f *fakeLedger) GetTransaction(_ context.Context, id uuid.UUID) (ledger.Transaction, error) {
	if f.getTransaction == nil {
		return ledger.Transaction{}, errNotWired
	}
	return f.getTransaction(id)
}
func (f *fakeLedger) Reverse(_ context.Context, i service.Idempotency, id uuid.UUID) (service.TransactionResult, error) {
	if f.reverse == nil {
		return service.TransactionResult{}, errNotWired
	}
	return f.reverse(i, id)
}

func newAPI(t *testing.T, f *fakeLedger, rec Reconciler) http.Handler {
	t.Helper()
	logs := &bytes.Buffer{}
	return New(Options{
		Logger:       slogDiscard(logs),
		APIKey:       config.Secret(testKey),
		MaxBodyBytes: 1 << 16,
	}, Deps{Ledger: f, Reconcile: rec})
}

var tsAt = time.Date(2026, 10, 13, 15, 4, 5, 123456000, time.UTC)

func sampleAccount() ledger.Account {
	last := tsAt.Add(time.Minute)
	return ledger.Account{
		ID: idA, Code: "user-wallet", Currency: "USD", Type: ledger.Liability, NormalBalance: ledger.Credit,
		AllowNegative: false, Balance: big.NewInt(12345), LastPostingAt: &last, CreatedAt: tsAt,
	}
}

func sampleTransaction() ledger.Transaction {
	mk := func(id int64, acct uuid.UUID, side ledger.Side, bal string) ledger.Posting {
		a, _ := ledger.ParseAmount("9223372036854775809")
		b, _ := new(big.Int).SetString(bal, 10)
		return ledger.Posting{ID: id, TransactionID: idT, AccountID: acct, Direction: side, Amount: a, Currency: "ETH", BalanceAfter: b, RecordedAt: tsAt}
	}
	return ledger.Transaction{
		ID: idT, Description: "deposit", CreatedAt: tsAt,
		Postings: []ledger.Posting{mk(1, idA, ledger.Debit, "9223372036854775809"), mk(2, idB, ledger.Credit, "9223372036854775809")},
	}
}

const validTx = `{"description":"deposit","postings":[
 {"account_id":"00000000-0000-0000-0000-00000000000a","direction":"DEBIT","amount":"10000","currency":"USD"},
 {"account_id":"00000000-0000-0000-0000-00000000000b","direction":"CREDIT","amount":"10000","currency":"USD"}]}`

// ---- accounts ----

func TestCreateAccount(t *testing.T) {
	var got ledger.NewAccount
	h := newAPI(t, &fakeLedger{createAccount: func(n ledger.NewAccount) (ledger.Account, error) {
		got = n
		return sampleAccount(), nil
	}}, nil)

	rec := call(h, "POST", "/v1/accounts", `{"code":"user-wallet","currency":"USD","type":"LIABILITY","normal_balance":"CREDIT","allow_negative":false}`)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	want := ledger.NewAccount{Code: "user-wallet", Currency: "USD", Type: ledger.Liability, NormalBalance: ledger.Credit}
	if got != want {
		t.Errorf("service received %+v, want %+v", got, want)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/accounts/"+idA.String() {
		t.Errorf("Location = %q", loc)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	expect := map[string]any{
		"id": idA.String(), "code": "user-wallet", "currency": "USD", "type": "LIABILITY", "normal_balance": "CREDIT",
		"allow_negative": false, "balance": "12345", "last_posting_at": "2026-10-13T15:05:05.123456Z", "created_at": "2026-10-13T15:04:05.123456Z",
	}
	for k, v := range expect {
		if body[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, body[k], body[k], v)
		}
	}
	if len(body) != len(expect) {
		t.Errorf("unexpected fields in the response: %v", body)
	}
}

func TestCreateAccount_Errors(t *testing.T) {
	h := newAPI(t, &fakeLedger{createAccount: func(n ledger.NewAccount) (ledger.Account, error) {
		switch n.Code {
		case "dup":
			return ledger.Account{}, fmt.Errorf("%w: %q", ledger.ErrAccountCodeExists, n.Code)
		case "bad":
			return ledger.Account{}, n.Validate()
		}
		return ledger.Account{}, errors.New("boom: pq: connection refused at 10.0.0.5:5432")
	}}, nil)

	mustProblem(t, call(h, "POST", "/v1/accounts", `{"code":"dup","currency":"USD","type":"ASSET","normal_balance":"DEBIT"}`), 409, CodeAccountCodeExists)

	p := mustProblem(t, call(h, "POST", "/v1/accounts", `{"code":"bad","currency":"usd","type":"X","normal_balance":"Y"}`), 422, CodeValidationError)
	fields := map[string]bool{}
	for _, e := range p.Errors {
		fields[e.Field] = true
	}
	for _, f := range []string{"currency", "type", "normal_balance"} {
		if !fields[f] {
			t.Errorf("no field error for %q: %+v", f, p.Errors)
		}
	}

	rec := call(h, "POST", "/v1/accounts", `{"code":"x"}`)
	mustProblem(t, rec, 500, CodeInternalError)
	if strings.Contains(rec.Body.String(), "10.0.0.5") || strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("internal error text leaked: %s", rec.Body)
	}

	mustProblem(t, call(h, "POST", "/v1/accounts", `{"code":"a","typo":1}`), 400, CodeMalformedRequest)
	mustProblem(t, call(h, "POST", "/v1/accounts", `{"code":"a"`), 400, CodeMalformedRequest)
	mustProblem(t, call(h, "POST", "/v1/accounts", `{"code":"a"}`, "Content-Type", "text/plain"), 415, CodeUnsupportedMediaType)
}

func TestGetAccount(t *testing.T) {
	h := newAPI(t, &fakeLedger{getAccount: func(id uuid.UUID) (ledger.Account, error) {
		if id == idB {
			return ledger.Account{}, &ledger.AccountNotFoundError{ID: id}
		}
		a := sampleAccount()
		a.LastPostingAt = nil
		return a, nil
	}}, nil)

	rec := call(h, "GET", "/v1/accounts/"+idA.String(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"last_posting_at":null`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mustProblem(t, call(h, "GET", "/v1/accounts/"+idB.String(), ""), 404, CodeNotFound)

	// Only the canonical UUID spelling is accepted.
	for _, bad := range []string{
		"123", "not-a-uuid", "00000000000000000000000000000a", "{00000000-0000-0000-0000-00000000000a}",
		"urn:uuid:00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000a0", "0000000000000-0000-0000-0000-00000000000",
	} {
		mustProblem(t, call(h, "GET", "/v1/accounts/"+bad, ""), 400, CodeMalformedRequest)
	}
	// Upper case is the same UUID.
	if rec := call(h, "GET", "/v1/accounts/"+strings.ToUpper(idA.String()), ""); rec.Code != 200 {
		t.Errorf("upper-case UUID: %d", rec.Code)
	}
}

func TestGetBalance(t *testing.T) {
	var gotAsOf *time.Time
	h := newAPI(t, &fakeLedger{balance: func(id uuid.UUID, asOf *time.Time) (service.Balance, error) {
		gotAsOf = asOf
		b := service.Balance{AccountID: id, Currency: "USD", Amount: big.NewInt(70), AsOf: asOf}
		return b, nil
	}}, nil)
	url := "/v1/accounts/" + idA.String() + "/balance"

	rec := call(h, "GET", url, "")
	if rec.Code != 200 || gotAsOf != nil || !strings.Contains(rec.Body.String(), `"as_of":null`) || !strings.Contains(rec.Body.String(), `"balance":"70"`) {
		t.Fatalf("current balance: %d %s (asOf=%v)", rec.Code, rec.Body, gotAsOf)
	}

	rec = call(h, "GET", url+"?as_of=2026-10-01T10:00:00.5Z", "")
	if rec.Code != 200 || gotAsOf == nil || !gotAsOf.Equal(time.Date(2026, 10, 1, 10, 0, 0, 500_000_000, time.UTC)) {
		t.Fatalf("as_of: %d %s (asOf=%v)", rec.Code, rec.Body, gotAsOf)
	}
	if !strings.Contains(rec.Body.String(), `"as_of":"2026-10-01T10:00:00.500000Z"`) {
		t.Errorf("as_of must be echoed in UTC: %s", rec.Body)
	}

	// An offset is converted, not ignored.
	call(h, "GET", url+"?as_of=2026-10-01T10:00:00-04:00", "")
	if !gotAsOf.Equal(time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)) {
		t.Errorf("offset not honoured: %v", gotAsOf)
	}

	for _, bad := range []string{"as_of=", "as_of=yesterday", "as_of=2026-10-01", "as_of=1759312800", "as_of=2026-13-01T00:00:00Z", "as_of=2026-10-01T10:00:00"} {
		p := mustProblem(t, call(h, "GET", url+"?"+bad, ""), 422, CodeValidationError)
		if len(p.Errors) != 1 || p.Errors[0].Field != "as_of" {
			t.Errorf("%s: %+v", bad, p.Errors)
		}
	}
	mustProblem(t, call(h, "GET", url+"?asof=2026-10-01T10:00:00Z", ""), 400, CodeMalformedRequest)
	mustProblem(t, call(h, "GET", url+"?as_of=2026-10-01T10:00:00Z&as_of=2026-10-02T10:00:00Z", ""), 400, CodeMalformedRequest)
}

func TestListPostings(t *testing.T) {
	var gotCursor string
	var gotLimit int
	h := newAPI(t, &fakeLedger{listPostings: func(_ uuid.UUID, cursor string, limit int) (service.PostingPage, error) {
		gotCursor, gotLimit = cursor, limit
		if cursor == "empty" {
			return service.PostingPage{Postings: []ledger.Posting{}}, nil
		}
		return service.PostingPage{Postings: sampleTransaction().Postings, NextCursor: "next-page"}, nil
	}}, nil)
	url := "/v1/accounts/" + idA.String() + "/postings"

	rec := call(h, "GET", url, "")
	if rec.Code != 200 || gotLimit != 0 || gotCursor != "" || !strings.Contains(rec.Body.String(), `"next_cursor":"next-page"`) {
		t.Fatalf("%d %s limit=%d cursor=%q", rec.Code, rec.Body, gotLimit, gotCursor)
	}
	call(h, "GET", url+"?limit=200&cursor=abc", "")
	if gotLimit != 200 || gotCursor != "abc" {
		t.Errorf("limit=%d cursor=%q", gotLimit, gotCursor)
	}
	call(h, "GET", url+"?limit=1", "")
	if gotLimit != 1 {
		t.Errorf("limit=%d", gotLimit)
	}

	rec = call(h, "GET", url+"?cursor=empty", "")
	if !strings.Contains(rec.Body.String(), `"postings":[]`) || !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Errorf("an empty page must be [] and a null cursor: %s", rec.Body)
	}

	for _, bad := range []string{"limit=0", "limit=-1", "limit=201", "limit=abc", "limit=", "limit=1.5", "limit=1e2", "limit=%2B5x"} {
		p := mustProblem(t, call(h, "GET", url+"?"+bad, ""), 422, CodeValidationError)
		if len(p.Errors) != 1 || p.Errors[0].Field != "limit" {
			t.Errorf("%s: %+v", bad, p.Errors)
		}
	}
	p := mustProblem(t, call(h, "GET", url+"?cursor=", ""), 422, CodeValidationError)
	if p.Errors[0].Field != "cursor" {
		t.Errorf("%+v", p.Errors)
	}
	mustProblem(t, call(h, "GET", url+"?page=2", ""), 400, CodeMalformedRequest)
}

// ---- transactions ----

func TestCreateTransaction(t *testing.T) {
	var gotIdem service.Idempotency
	var gotReq ledger.TransactionRequest
	h := newAPI(t, &fakeLedger{createTransaction: func(i service.Idempotency, r ledger.TransactionRequest) (service.TransactionResult, error) {
		gotIdem, gotReq = i, r
		return service.TransactionResult{Transaction: sampleTransaction()}, nil
	}}, nil)

	rec := call(h, "POST", "/v1/transactions", validTx, "Idempotency-Key", "key-1")
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Location") != "/v1/transactions/"+idT.String() || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Errorf("headers: %v", rec.Header())
	}
	if gotIdem.Key != "key-1" || len(gotReq.Postings) != 2 || gotReq.Description != "deposit" {
		t.Fatalf("service received key=%q request=%+v", gotIdem.Key, gotReq)
	}
	p0 := gotReq.Postings[0]
	if p0.AccountID != idA || p0.Direction != ledger.Debit || p0.Amount.String() != "10000" || p0.Currency != "USD" {
		t.Errorf("first posting = %+v", p0)
	}

	// Money is always a JSON string in responses, even beyond 2^63.
	body := rec.Body.String()
	if !strings.Contains(body, `"amount":"9223372036854775809"`) || !strings.Contains(body, `"balance_after":"9223372036854775809"`) {
		t.Errorf("amounts must be strings: %s", body)
	}
	if regexp.MustCompile(`"(amount|balance_after)":\s*[0-9]`).MatchString(body) {
		t.Errorf("a money field was serialised as a JSON number: %s", body)
	}
	if !strings.Contains(body, `"reverses_id":null`) || !strings.Contains(body, `"recorded_at":"2026-10-13T15:04:05.123456Z"`) {
		t.Errorf("unexpected shape: %s", body)
	}
}

func TestCreateTransaction_ReplayIsFlagged(t *testing.T) {
	h := newAPI(t, &fakeLedger{createTransaction: func(service.Idempotency, ledger.TransactionRequest) (service.TransactionResult, error) {
		return service.TransactionResult{Transaction: sampleTransaction(), Replayed: true}, nil
	}}, nil)
	rec := call(h, "POST", "/v1/transactions", validTx, "Idempotency-Key", "k")
	if rec.Code != 201 || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("a replay answers like the original plus a header: %d %v", rec.Code, rec.Header())
	}
}

func TestCreateTransaction_AmountMustBeAString(t *testing.T) {
	h := newAPI(t, &fakeLedger{}, nil)
	post := func(amount string) *httptest.ResponseRecorder {
		body := `{"postings":[
		 {"account_id":"` + idA.String() + `","direction":"DEBIT","amount":` + amount + `,"currency":"USD"},
		 {"account_id":"` + idB.String() + `","direction":"CREDIT","amount":"1","currency":"USD"}]}`
		return call(h, "POST", "/v1/transactions", body, "Idempotency-Key", "k")
	}

	for name, amount := range map[string]string{
		"JSON number":            `10000`,
		"JSON float":             `100.5`,
		"huge JSON number":       `9223372036854775809`,
		"exponent number":        `1e5`,
		"null":                   `null`,
		"boolean":                `true`,
		"array":                  `["1"]`,
		"empty string":           `""`,
		"zero":                   `"0"`,
		"negative":               `"-5"`,
		"leading zeros":          `"007"`,
		"decimal":                `"1.50"`,
		"above 10^30":            `"1000000000000000000000000000001"`,
		"string with whitespace": `" 5"`,
	} {
		t.Run(name, func(t *testing.T) {
			p := mustProblem(t, post(amount), 422, CodeValidationError)
			if len(p.Errors) != 1 || p.Errors[0].Field != "postings[0].amount" {
				t.Errorf("want one error on postings[0].amount, got %+v", p.Errors)
			}
		})
	}

	t.Run("missing amount", func(t *testing.T) {
		body := `{"postings":[{"account_id":"` + idA.String() + `","direction":"DEBIT","currency":"USD"}]}`
		p := mustProblem(t, call(h, "POST", "/v1/transactions", body, "Idempotency-Key", "k"), 422, CodeValidationError)
		if p.Errors[0].Field != "postings[0].amount" {
			t.Errorf("%+v", p.Errors)
		}
	})
}

func TestCreateTransaction_ShapeErrorsAreAllReported(t *testing.T) {
	h := newAPI(t, &fakeLedger{}, nil)
	body := `{"postings":[
	  {"account_id":"nope","direction":"DEBIT","amount":5,"currency":"USD"},
	  {"direction":"CREDIT","amount":"1","currency":"USD"}]}`
	p := mustProblem(t, call(h, "POST", "/v1/transactions", body, "Idempotency-Key", "k"), 422, CodeValidationError)
	got := map[string]string{}
	for _, e := range p.Errors {
		got[e.Field] = e.Message
	}
	for _, f := range []string{"postings[0].account_id", "postings[0].amount", "postings[1].account_id"} {
		if got[f] == "" {
			t.Errorf("missing error for %s: %+v", f, p.Errors)
		}
	}
}

func TestCreateTransaction_IdempotencyHeader(t *testing.T) {
	var got service.Idempotency
	h := newAPI(t, &fakeLedger{createTransaction: func(i service.Idempotency, _ ledger.TransactionRequest) (service.TransactionResult, error) {
		got = i
		if i.Key == "" {
			return service.TransactionResult{}, &ledger.ValidationError{Violations: []ledger.Violation{{Field: "Idempotency-Key", Message: "is required"}}}
		}
		return service.TransactionResult{Transaction: sampleTransaction()}, nil
	}}, nil)

	p := mustProblem(t, call(h, "POST", "/v1/transactions", validTx), 422, CodeValidationError)
	if p.Errors[0].Field != "Idempotency-Key" {
		t.Errorf("%+v", p.Errors)
	}

	// Two header lines for the same key are ambiguous and refused.
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/transactions", strings.NewReader(validTx))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Idempotency-Key", "a")
	req.Header.Add("Idempotency-Key", "b")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	p = mustProblem(t, rec, 422, CodeValidationError)
	if p.Errors[0].Field != "Idempotency-Key" {
		t.Errorf("%+v", p.Errors)
	}
	_ = got
}

// The same logical request must always hash the same, however the client
// formats its JSON; any semantic difference must change the hash.
func TestCreateTransaction_RequestHashIsCanonical(t *testing.T) {
	var hashes [][]byte
	h := newAPI(t, &fakeLedger{createTransaction: func(i service.Idempotency, _ ledger.TransactionRequest) (service.TransactionResult, error) {
		hashes = append(hashes, i.RequestHash)
		return service.TransactionResult{Transaction: sampleTransaction()}, nil
	}}, nil)

	send := func(body string) {
		if rec := call(h, "POST", "/v1/transactions", body, "Idempotency-Key", "k"); rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	a, b := strings.ToUpper(idA.String()), idB.String()

	send(validTx) // reference
	// Same request: different key order, whitespace, UUID case, and an unneeded escape.
	send(`{ "postings": [ {"currency":"USD","amount":"10000","direction":"DEBIT","account_id":"` + a + `"},
	       {"amount":"10000","account_id":"` + b + `","currency":"USD","direction":"CREDIT"} ], "description": "dep\u006fsit" }`)
	if string(hashes[0]) != string(hashes[1]) {
		t.Error("formatting differences must not change the request hash")
	}

	variants := map[string]string{
		"different amount":      strings.Replace(validTx, `"10000","currency":"USD"}`, `"10001","currency":"USD"}`, 1),
		"different description": strings.Replace(validTx, `"deposit"`, `"deposit2"`, 1),
		"different account":     strings.Replace(validTx, "00000000000a", "00000000000c", 1),
		"different direction":   strings.Replace(validTx, `"DEBIT"`, `"CREDIT"`, 1),
		"different currency":    strings.Replace(validTx, `"USD"}`, `"EUR"}`, 1),
		"swapped posting order": `{"description":"deposit","postings":[
		  {"account_id":"` + idB.String() + `","direction":"CREDIT","amount":"10000","currency":"USD"},
		  {"account_id":"` + idA.String() + `","direction":"DEBIT","amount":"10000","currency":"USD"}]}`,
	}
	for name, body := range variants {
		send(body)
		last := hashes[len(hashes)-1]
		if string(last) == string(hashes[0]) {
			t.Errorf("%s must change the request hash", name)
		}
	}
}

func TestGetTransaction(t *testing.T) {
	h := newAPI(t, &fakeLedger{getTransaction: func(id uuid.UUID) (ledger.Transaction, error) {
		if id != idT {
			return ledger.Transaction{}, &ledger.TransactionNotFoundError{ID: id}
		}
		tx := sampleTransaction()
		tx.ReversesID = &idA
		return tx, nil
	}}, nil)

	rec := call(h, "GET", "/v1/transactions/"+idT.String(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"reverses_id":"`+idA.String()+`"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mustProblem(t, call(h, "GET", "/v1/transactions/"+idB.String(), ""), 404, CodeNotFound)
}

func TestReverseTransaction(t *testing.T) {
	var got service.Idempotency
	var gotID uuid.UUID
	h := newAPI(t, &fakeLedger{reverse: func(i service.Idempotency, id uuid.UUID) (service.TransactionResult, error) {
		got, gotID = i, id
		switch id {
		case idA:
			return service.TransactionResult{}, ledger.ErrAlreadyReversed
		case idB:
			return service.TransactionResult{}, &ledger.InsufficientFundsError{AccountID: idA, Shortfall: "10"}
		}
		return service.TransactionResult{Transaction: sampleTransaction(), Replayed: true}, nil
	}}, nil)

	rec := call(h, "POST", "/v1/transactions/"+idT.String()+"/reverse", "", "Idempotency-Key", "r-1")
	if rec.Code != 201 || got.Key != "r-1" || gotID != idT || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("%d %s key=%q id=%s", rec.Code, rec.Body, got.Key, gotID)
	}

	// The hash depends on the transaction id, normalised to lower case, and on nothing else.
	lower := got.RequestHash
	call(h, "POST", "/v1/transactions/"+strings.ToUpper(idT.String())+"/reverse", "", "Idempotency-Key", "r-1")
	if string(got.RequestHash) != string(lower) {
		t.Error("the UUID's letter case must not change the request hash")
	}
	call(h, "POST", "/v1/transactions/"+idT.String()+"/reverse", `{"ignored":true}`, "Idempotency-Key", "r-1")
	if string(got.RequestHash) != string(lower) {
		t.Error("a body must not change the request hash")
	}
	other := idT
	other[15]++
	call(h, "POST", "/v1/transactions/"+other.String()+"/reverse", "", "Idempotency-Key", "r-1")
	if string(got.RequestHash) == string(lower) {
		t.Error("another transaction must change the request hash")
	}

	mustProblem(t, call(h, "POST", "/v1/transactions/"+idA.String()+"/reverse", "", "Idempotency-Key", "r-2"), 409, CodeAlreadyReversed)
	mustProblem(t, call(h, "POST", "/v1/transactions/"+idB.String()+"/reverse", "", "Idempotency-Key", "r-3"), 422, CodeInsufficientFunds)
	mustProblem(t, call(h, "POST", "/v1/transactions/xyz/reverse", "", "Idempotency-Key", "r-4"), 400, CodeMalformedRequest)
}

// ---- reconciliation ----

func TestReconcile(t *testing.T) {
	var gotDeep bool
	report := ledger.ReconciliationReport{
		StartedAt: tsAt, AccountsChecked: 3,
		Mismatches:  []ledger.BalanceMismatch{{AccountID: idA, Code: "w", Currency: "USD", Stored: big.NewInt(8), Expected: big.NewInt(5)}},
		Currencies:  []ledger.CurrencyTotals{{Currency: "USD", Debits: big.NewInt(10), Credits: big.NewInt(9)}, {Currency: "ETH", Debits: big.NewInt(4), Credits: big.NewInt(4)}},
		ChainBreaks: []ledger.ChainBreak{{PostingID: 7, AccountID: idB, Recorded: big.NewInt(1), Expected: big.NewInt(2)}},
	}
	h := newAPI(t, &fakeLedger{}, func(_ context.Context, deep bool) (ledger.ReconciliationReport, error) {
		gotDeep = deep
		report.Deep = deep
		return report, nil
	})

	rec := call(h, "POST", "/v1/reconciliations?deep=true", "")
	if rec.Code != 200 || !gotDeep {
		t.Fatalf("%d %s deep=%v", rec.Code, rec.Body, gotDeep)
	}
	var out reconciliationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || !out.Deep || out.AccountsChecked != 3 || len(out.Mismatches) != 1 || out.Mismatches[0].Stored != "8" ||
		out.Currencies[0].Balanced || !out.Currencies[1].Balanced || out.ChainBreaks[0].PostingID != 7 {
		t.Errorf("unexpected report: %+v", out)
	}

	call(h, "POST", "/v1/reconciliations", "")
	if gotDeep {
		t.Error("deep defaults to false")
	}
	call(h, "POST", "/v1/reconciliations?deep=false", "")
	if gotDeep {
		t.Error("deep=false")
	}
	for _, bad := range []string{"deep=yes", "deep=1", "deep=TRUE", "deep="} {
		mustProblem(t, call(h, "POST", "/v1/reconciliations?"+bad, ""), 422, CodeValidationError)
	}
	mustProblem(t, call(h, "POST", "/v1/reconciliations?mode=full", ""), 400, CodeMalformedRequest)

	clean := newAPI(t, &fakeLedger{}, func(context.Context, bool) (ledger.ReconciliationReport, error) {
		return ledger.ReconciliationReport{}, nil
	})
	rec = call(clean, "POST", "/v1/reconciliations", "")
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"mismatches":[]`) {
		t.Errorf("a clean report: %s", rec.Body)
	}
}

// ---- health ----

func TestHealthAndReadiness(t *testing.T) {
	logs := &bytes.Buffer{}
	build := func(ready func(context.Context) error) http.Handler {
		return New(Options{
			Logger: slogDiscard(logs), APIKey: config.Secret(testKey), ReadyTimeout: 50 * time.Millisecond,
		}, Deps{Ledger: &fakeLedger{}, Ready: ready})
	}

	t.Run("a service without dependencies is ready", func(t *testing.T) {
		h := build(nil)
		for _, path := range []string{"/healthz", "/readyz"} {
			rec := call(h, "GET", path, "", "Authorization", "") // no API key
			if rec.Code != 200 {
				t.Errorf("%s: %d", path, rec.Code)
			}
		}
	})

	t.Run("not ready when a dependency fails; alive regardless", func(t *testing.T) {
		h := build(func(context.Context) error { return errors.New("dial tcp 10.0.0.5:5432: connect: connection refused") })
		rec := call(h, "GET", "/readyz", "", "Authorization", "")
		mustProblem(t, rec, 503, CodeNotReady)
		if strings.Contains(rec.Body.String(), "10.0.0.5") || strings.Contains(rec.Body.String(), "refused") {
			t.Errorf("the dependency error leaked: %s", rec.Body)
		}
		if !strings.Contains(logs.String(), "connection refused") {
			t.Error("the reason must be logged")
		}
		if rec := call(h, "GET", "/healthz", "", "Authorization", ""); rec.Code != 200 {
			t.Errorf("liveness must not depend on dependencies: %d", rec.Code)
		}
	})

	t.Run("a slow dependency counts as not ready, quickly", func(t *testing.T) {
		h := build(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
		start := time.Now()
		mustProblem(t, call(h, "GET", "/readyz", "", "Authorization", ""), 503, CodeNotReady)
		if time.Since(start) > 2*time.Second {
			t.Error("the readiness check was not bounded by its timeout")
		}
	})

	t.Run("the ready body", func(t *testing.T) {
		rec := call(build(func(context.Context) error { return nil }), "GET", "/readyz", "", "Authorization", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ready"`) {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}
