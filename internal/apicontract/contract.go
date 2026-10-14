// Package apicontract checks the running API against docs/openapi.yaml. It is
// only used by tests: the specification is the contract, and any difference
// between it and the implementation is a bug in one of them.
package apicontract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// ErrUndocumented means the request does not match any operation in the
// specification (an unknown path or method). Callers may treat that as
// "nothing to check", as the 404 and 405 answers for such requests are generic.
var ErrUndocumented = errors.New("the request is not described by the specification")

func init() {
	// Problem documents are JSON with a structured-syntax suffix.
	openapi3filter.RegisterBodyDecoder("application/problem+json", openapi3filter.JSONBodyDecoder)
}

// Contract is a loaded and validated specification.
type Contract struct {
	Doc    *openapi3.T
	router routers.Router
}

// specURL is the server address the specification declares.
const specURL = "http://localhost:8085"

// Path returns the location of docs/openapi.yaml.
func Path() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "docs", "openapi.yaml")
}

// Load reads and validates the specification.
func Load() (*Contract, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(Path())
	if err != nil {
		return nil, fmt.Errorf("load specification: %w", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("the specification is not valid OpenAPI: %w", err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router from specification: %w", err)
	}
	return &Contract{Doc: doc, router: router}, nil
}

// Operations lists every documented operation as "METHOD /path/{param}", sorted.
func (c *Contract) Operations() []string {
	var ops []string
	for path, item := range c.Doc.Paths.Map() {
		for method := range item.Operations() {
			ops = append(ops, method+" "+path)
		}
	}
	sort.Strings(ops)
	return ops
}

// ErrorCodes lists the values of the ErrorCode schema, sorted.
func (c *Contract) ErrorCodes() []string {
	ref, ok := c.Doc.Components.Schemas["ErrorCode"]
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(ref.Value.Enum))
	for _, v := range ref.Value.Enum {
		if s, ok := v.(string); ok {
			codes = append(codes, s)
		}
	}
	slices.Sort(codes)
	return codes
}

// Exchange is one request and the response it received.
type Exchange struct {
	Method        string
	PathAndQuery  string // for example /v1/accounts/{uuid}/balance?as_of=...
	RequestHeader http.Header
	RequestBody   []byte

	Status         int
	ResponseHeader http.Header
	ResponseBody   []byte

	// CheckRequest also validates the request against the specification. It is
	// meant for exchanges that succeeded: anything the API accepted must be
	// something the specification allows.
	CheckRequest bool
}

// Check validates the response (and optionally the request) of an exchange. The
// response status must be documented for the operation, its headers and body
// must match, and bodies may not carry undocumented fields.
func (c *Contract) Check(ctx context.Context, ex Exchange) error {
	u, err := url.Parse(specURL + ex.PathAndQuery)
	if err != nil {
		return fmt.Errorf("bad path %q: %w", ex.PathAndQuery, err)
	}
	var body io.Reader
	if len(ex.RequestBody) > 0 {
		body = bytes.NewReader(ex.RequestBody)
	}
	req, err := http.NewRequestWithContext(ctx, ex.Method, u.String(), body)
	if err != nil {
		return err
	}
	for k, v := range ex.RequestHeader {
		req.Header[k] = v
	}

	route, pathParams, err := c.router.FindRoute(req)
	if err != nil {
		// The router's sentinel errors are compared by their text: the legacy
		// router returns fresh *RouteError values with the same reason.
		var re *routers.RouteError
		if errors.As(err, &re) && (re.Reason == routers.ErrPathNotFound.Error() || re.Reason == routers.ErrMethodNotAllowed.Error()) {
			return fmt.Errorf("%w: %s %s", ErrUndocumented, ex.Method, ex.PathAndQuery)
		}
		return fmt.Errorf("find route: %w", err)
	}

	reqInput := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if ex.CheckRequest {
		if err := openapi3filter.ValidateRequest(ctx, reqInput); err != nil {
			return fmt.Errorf("the request does not match the specification: %w", err)
		}
	}

	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 ex.Status,
		Header:                 ex.ResponseHeader,
		Body:                   io.NopCloser(bytes.NewReader(ex.ResponseBody)),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(ctx, respInput); err != nil {
		return fmt.Errorf("the response does not match the specification (status %d): %w", ex.Status, err)
	}
	return nil
}

// Describe summarises an exchange for failure messages.
func (ex Exchange) Describe() string {
	return strings.TrimSpace(fmt.Sprintf("%s %s -> %d", ex.Method, ex.PathAndQuery, ex.Status))
}
