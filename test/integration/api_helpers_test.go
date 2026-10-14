package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/api"
	"github.com/radadev/ledgerd/internal/apicontract"
	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/reconcile"
	"github.com/radadev/ledgerd/internal/service"
	"github.com/radadev/ledgerd/internal/store"
)

const apiKey = "integration-test-api-key-0123456789"

// apiEnv is a real HTTP server in front of the real service and database.
type apiEnv struct {
	*env
	srv      *httptest.Server
	contract *apicontract.Contract
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	database := server.NewDB(t)
	st := store.New(database.App, store.Config{})
	svc := service.New(st)

	handler := api.New(api.Options{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKey:       config.Secret(apiKey),
		MaxBodyBytes: 1 << 20,
	}, api.Deps{
		Ledger: svc,
		Reconcile: func(ctx context.Context, deep bool) (ledger.ReconciliationReport, error) {
			return reconcile.Run(ctx, st, reconcile.Options{Deep: deep})
		},
		Ready: func(ctx context.Context) error { return database.App.Ping(ctx) },
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	contract, err := apicontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &apiEnv{env: &env{db: database, st: st, svc: svc, clock: newFakeClock()}, srv: srv, contract: contract}
}

// response is a fully read HTTP response.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r response) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, r.Body)
	}
	return m
}

// do sends a request with the API key. Header pairs override or (with an empty
// value) remove headers.
func (a *apiEnv) do(t testing.TB, method, path, body string, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	res, err := a.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close() //nolint:errcheck // test helper
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	a.checkContract(t, req, body, res, b)
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

// checkContract validates every exchange of every API test against
// docs/openapi.yaml, so the whole API suite doubles as a contract test. The
// response must always conform; the request must as well when the API accepted
// it. Paths and methods the specification does not describe (generic 404/405
// answers) are skipped.
func (a *apiEnv) checkContract(t testing.TB, req *http.Request, reqBody string, res *http.Response, resBody []byte) {
	t.Helper()
	ex := apicontract.Exchange{
		Method:         req.Method,
		PathAndQuery:   req.URL.RequestURI(),
		RequestHeader:  req.Header,
		RequestBody:    []byte(reqBody),
		Status:         res.StatusCode,
		ResponseHeader: res.Header,
		ResponseBody:   resBody,
		CheckRequest:   res.StatusCode < 400,
	}
	if err := a.contract.Check(context.Background(), ex); err != nil && !errors.Is(err, apicontract.ErrUndocumented) {
		t.Errorf("CONTRACT VIOLATION: %s\n%v\nresponse body: %s", ex.Describe(), err, resBody)
	}
}

func (a *apiEnv) mustStatus(t testing.TB, r response, want int) response {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status = %d, want %d\n%s", r.Status, want, r.Body)
	}
	return r
}

// createAccount creates an account over HTTP and returns its id.
func (a *apiEnv) createAccount(t testing.TB, code, currency, typ, normal string, allowNegative bool) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"code": code, "currency": currency, "type": typ, "normal_balance": normal, "allow_negative": allowNegative,
	})
	r := a.mustStatus(t, a.do(t, "POST", "/v1/accounts", string(body)), 201)
	return r.json(t)["id"].(string)
}

// txBody builds a two-posting transaction body.
func txBody(debit, credit, amount, currency string) string {
	b, _ := json.Marshal(map[string]any{
		"description": "test",
		"postings": []map[string]any{
			{"account_id": debit, "direction": "DEBIT", "amount": amount, "currency": currency},
			{"account_id": credit, "direction": "CREDIT", "amount": amount, "currency": currency},
		},
	})
	return string(b)
}

func mustParseUUID(t testing.TB, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
