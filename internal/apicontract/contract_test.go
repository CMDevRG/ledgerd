package apicontract_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/radadev/ledgerd/internal/api"
	"github.com/radadev/ledgerd/internal/apicontract"
)

func load(t *testing.T) *apicontract.Contract {
	t.Helper()
	c, err := apicontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSpecificationIsValidOpenAPI(t *testing.T) {
	load(t) // Load validates the document
}

// Every endpoint the server registers is documented, and every documented
// operation exists. Neither side can change without the other.
func TestRoutesMatchTheSpecification(t *testing.T) {
	spec := load(t).Operations()
	impl := api.RoutePatterns()
	slices.Sort(impl)

	if !slices.Equal(spec, impl) {
		t.Errorf("the specification and the server disagree.\n spec:   %v\n server: %v", spec, impl)
	}
}

func TestErrorCodesMatchTheSpecification(t *testing.T) {
	spec, impl := load(t).ErrorCodes(), api.ErrorCodes()
	if !slices.Equal(spec, impl) {
		t.Errorf("error codes differ.\n spec:   %v\n server: %v", spec, impl)
	}
}

// The checks below prove the validator has teeth: a response that drifts from
// the contract must be reported, not waved through.
func TestValidatorCatchesDrift(t *testing.T) {
	c := load(t)
	jsonHdr := http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"abc"}}
	problemHdr := http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {"abc"}}

	const id = "3f2b8a52-2f8e-4c61-9a4e-0d6f4f0e7c11"
	account := func(extra string) []byte {
		return []byte(`{"id":"` + id + `","code":"c","currency":"USD","type":"ASSET","normal_balance":"DEBIT",` +
			`"allow_negative":false,"balance":"5","last_posting_at":null,"created_at":"2026-10-13T15:04:05.123456Z"` + extra + `}`)
	}
	get := func(status int, hdr http.Header, body []byte) apicontract.Exchange {
		return apicontract.Exchange{Method: "GET", PathAndQuery: "/v1/accounts/" + id, Status: status, ResponseHeader: hdr, ResponseBody: body}
	}

	ok := get(200, jsonHdr, account(""))
	if err := c.Check(context.Background(), ok); err != nil {
		t.Fatalf("a conforming response was rejected: %v", err)
	}

	drift := map[string]apicontract.Exchange{
		"an undocumented extra field":   get(200, jsonHdr, account(`,"secret":"x"`)),
		"a balance as a JSON number":    get(200, jsonHdr, []byte(strings.Replace(string(account("")), `"balance":"5"`, `"balance":5`, 1))),
		"a lower-case currency":         get(200, jsonHdr, []byte(strings.Replace(string(account("")), `"USD"`, `"usd"`, 1))),
		"a missing required field":      get(200, jsonHdr, []byte(strings.Replace(string(account("")), `"code":"c",`, ``, 1))),
		"a status the operation lacks":  get(418, problemHdr, []byte(`{"type":"t","title":"t","status":418,"code":"not_found"}`)),
		"a missing X-Request-Id":        get(200, http.Header{"Content-Type": {"application/json"}}, account("")),
		"an unknown error code":         get(404, problemHdr, []byte(`{"type":"t","title":"t","status":404,"code":"teapot"}`)),
		"a problem with an extra field": get(404, problemHdr, []byte(`{"type":"t","title":"t","status":404,"code":"not_found","stack":"..."}`)),
		"an error served as plain JSON": get(404, jsonHdr, []byte(`{"type":"t","title":"t","status":404,"code":"not_found"}`)),
		"an oversized balance":          get(200, jsonHdr, []byte(strings.Replace(string(account("")), `"balance":"5"`, `"balance":"`+strings.Repeat("9", 40)+`"`, 1))),
	}
	for name, ex := range drift {
		if err := c.Check(context.Background(), ex); err == nil {
			t.Errorf("%s was not detected (%s)", name, ex.Describe())
		}
	}

	t.Run("requests are checked too", func(t *testing.T) {
		post := func(body string, hdr http.Header) apicontract.Exchange {
			return apicontract.Exchange{
				Method: "POST", PathAndQuery: "/v1/transactions", RequestHeader: hdr, RequestBody: []byte(body),
				Status: 422, ResponseHeader: problemHdr, ResponseBody: []byte(`{"type":"t","title":"t","status":422,"code":"validation_error"}`),
				CheckRequest: true,
			}
		}
		hdr := http.Header{"Content-Type": {"application/json"}, "Idempotency-Key": {"k"}}
		good := `{"postings":[{"account_id":"` + id + `","direction":"DEBIT","amount":"10","currency":"USD"},` +
			`{"account_id":"` + id + `","direction":"CREDIT","amount":"10","currency":"USD"}]}`
		if err := c.Check(context.Background(), post(good, hdr)); err != nil {
			t.Fatalf("a conforming request was rejected: %v", err)
		}
		asNumber := strings.Replace(good, `"amount":"10"`, `"amount":10`, 1)
		if err := c.Check(context.Background(), post(asNumber, hdr)); err == nil {
			t.Error("an amount sent as a JSON number must not match the request schema")
		}
		if err := c.Check(context.Background(), post(good, http.Header{"Content-Type": {"application/json"}})); err == nil {
			t.Error("a missing Idempotency-Key must not match the request schema")
		}
	})

	t.Run("unknown routes are reported as undocumented", func(t *testing.T) {
		err := c.Check(context.Background(), apicontract.Exchange{Method: "GET", PathAndQuery: "/v1/nothing", Status: 404})
		if !errors.Is(err, apicontract.ErrUndocumented) {
			t.Errorf("got %v", err)
		}
	})
}
