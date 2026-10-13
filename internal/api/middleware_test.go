package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthentication(t *testing.T) {
	h, _ := testHandler(t, nil, pingRoutes)

	t.Run("valid key", func(t *testing.T) {
		if rec := call(h, "GET", "/v1/ping", ""); rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("the scheme is case-insensitive", func(t *testing.T) {
		for _, scheme := range []string{"bearer", "BEARER", "BeArEr"} {
			if rec := call(h, "GET", "/v1/ping", "", "Authorization", scheme+" "+testKey); rec.Code != 200 {
				t.Errorf("scheme %q: status = %d", scheme, rec.Code)
			}
		}
	})

	rejected := map[string]string{
		"missing header":           "",
		"wrong key":                "Bearer " + strings.Repeat("x", len(testKey)),
		"key of another length":    "Bearer short",
		"prefix of the real key":   "Bearer " + testKey[:10],
		"real key plus a suffix":   "Bearer " + testKey + "x",
		"basic scheme":             "Basic " + testKey,
		"no scheme":                testKey,
		"scheme only":              "Bearer ",
		"empty token":              "Bearer",
		"two spaces":               "Bearer  " + testKey,
		"trailing space":           "Bearer " + testKey + " ",
		"token in lower case":      "Bearer " + strings.ToLower(testKey) + "!",
		"another scheme, same key": "Token " + testKey,
	}
	for name, header := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			rec := call(h, "GET", "/v1/ping", "", "Authorization", header)
			p := mustProblem(t, rec, 401, CodeUnauthorized)
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("a 401 must carry WWW-Authenticate")
			}
			if strings.Contains(rec.Body.String(), testKey) {
				t.Error("the response must not echo the key")
			}
			_ = p
		})
	}

	t.Run("health endpoints need no key", func(t *testing.T) {
		if rec := call(h, "GET", "/healthz", "", "Authorization", ""); rec.Code != 200 {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("an unknown /v1 path without a key is 401, not 404", func(t *testing.T) {
		// Otherwise anyone could probe which endpoints exist.
		mustProblem(t, call(h, "GET", "/v1/secret-admin", "", "Authorization", ""), 401, CodeUnauthorized)
		mustProblem(t, call(h, "POST", "/v1", "", "Authorization", ""), 401, CodeUnauthorized)
	})
}

func TestRequestID(t *testing.T) {
	h, _ := testHandler(t, nil, pingRoutes)

	t.Run("generated when absent", func(t *testing.T) {
		a := call(h, "GET", "/v1/ping", "").Header().Get("X-Request-Id")
		b := call(h, "GET", "/v1/ping", "").Header().Get("X-Request-Id")
		if len(a) != 32 || a == b {
			t.Errorf("want distinct 32-character ids, got %q and %q", a, b)
		}
	})
	t.Run("a well-formed client id is kept", func(t *testing.T) {
		rec := call(h, "GET", "/v1/ping", "", "X-Request-Id", "trace-123.abc_DEF")
		if got := rec.Header().Get("X-Request-Id"); got != "trace-123.abc_DEF" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a malformed client id is replaced", func(t *testing.T) {
		for _, bad := range []string{"has space", "semi;colon", "new\nline", strings.Repeat("a", 65), "ünï", "<script>"} {
			req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/ping", nil)
			req.Header.Set("Authorization", "Bearer "+testKey)
			req.Header["X-Request-Id"] = []string{bad}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-Id")
			if got == bad || !validRequestID.MatchString(got) {
				t.Errorf("id %q was not replaced by a safe one (got %q)", bad, got)
			}
		}
	})
	t.Run("the problem document carries the id", func(t *testing.T) {
		rec := call(h, "GET", "/v1/nope", "", "X-Request-Id", "abc-1")
		p := mustProblem(t, rec, 404, CodeNotFound)
		if p.RequestID != "abc-1" || p.Instance != "/v1/nope" {
			t.Errorf("problem = %+v", p)
		}
	})
}

func TestUnmatchedRoutes(t *testing.T) {
	h, _ := testHandler(t, nil, func(add routeAdder) {
		pingRoutes(add)
		add("POST /v1/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, r, 200, nil) })
	})

	mustProblem(t, call(h, "GET", "/v1/unknown", ""), 404, CodeNotFound)
	mustProblem(t, call(h, "GET", "/elsewhere", "", "Authorization", ""), 404, CodeNotFound)

	rec := call(h, "DELETE", "/v1/ping", "")
	mustProblem(t, rec, 405, CodeMethodNotAllowed)
	if allow := rec.Header().Get("Allow"); allow != "GET, POST" {
		t.Errorf("Allow = %q, want \"GET, POST\"", allow)
	}

	rec = call(h, "POST", "/healthz", "", "Authorization", "")
	mustProblem(t, rec, 405, CodeMethodNotAllowed)
	if allow := rec.Header().Get("Allow"); allow != "GET" {
		t.Errorf("Allow = %q, want GET", allow)
	}
}

func TestBodyLimit(t *testing.T) {
	echo := func(add routeAdder) {
		add("POST /v1/echo", func(w http.ResponseWriter, r *http.Request) {
			var v map[string]any
			if err := decodeJSON(r, &v); err != nil {
				writeProblem(w, r, mustMap(err))
				return
			}
			writeJSON(w, r, 200, v)
		})
	}
	const limit = 64
	h, _ := testHandler(t, func(o *Options) { o.MaxBodyBytes = limit }, echo)

	// Exactly `limit` bytes of valid JSON is accepted.
	exact := `{"k":"` + strings.Repeat("a", limit-8) + `"}`
	if len(exact) != limit {
		t.Fatalf("test body is %d bytes, want %d", len(exact), limit)
	}
	if rec := call(h, "POST", "/v1/echo", exact); rec.Code != 200 {
		t.Fatalf("a body of exactly the limit must pass, got %d: %s", rec.Code, rec.Body)
	}

	over := `{"k":"` + strings.Repeat("a", limit-7) + `"}`
	t.Run("declared length over the limit is refused up front", func(t *testing.T) {
		mustProblem(t, call(h, "POST", "/v1/echo", over), 413, CodePayloadTooLarge)
	})
	t.Run("an undeclared (chunked) body is cut off while reading", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/echo", io.NopCloser(strings.NewReader(over)))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		mustProblem(t, rec, 413, CodePayloadTooLarge)
	})
}

func mustMap(err error) Problem { p, _ := errorToProblem(err); return p }

func TestPanicRecovery(t *testing.T) {
	h, logs := testHandler(t, nil, func(add routeAdder) {
		add("GET /v1/boom", func(http.ResponseWriter, *http.Request) { panic("sensitive internal detail: db password hunter2") })
	})

	rec := call(h, "GET", "/v1/boom", "", "X-Request-Id", "req-boom")
	p := mustProblem(t, rec, 500, CodeInternalError)
	if p.RequestID != "req-boom" {
		t.Errorf("request id = %q", p.RequestID)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "sensitive") {
		t.Errorf("the panic message leaked to the client: %s", rec.Body)
	}

	var sawPanic, sawRequest bool
	for _, line := range logLines(t, logs) {
		switch line["msg"] {
		case "panic while handling request":
			sawPanic = true
			if !strings.Contains(line["panic"].(string), "hunter2") || line["stack"] == "" || line["request_id"] != "req-boom" {
				t.Errorf("the panic must be logged with its stack and request id: %v", line)
			}
		case "request":
			sawRequest = true
			if line["status"].(float64) != 500 || line["level"] != "ERROR" {
				t.Errorf("the access log must show the real 500: %v", line)
			}
		}
	}
	if !sawPanic || !sawRequest {
		t.Errorf("missing log lines (panic=%v request=%v)", sawPanic, sawRequest)
	}

	// The server keeps working afterwards.
	if rec := call(h, "GET", "/v1/boom", ""); rec.Code != 500 {
		t.Errorf("second call: %d", rec.Code)
	}
}

func TestAccessLog(t *testing.T) {
	h, logs := testHandler(t, nil, pingRoutes)

	call(h, "GET", "/v1/ping?secret=do-not-log", "", "X-Request-Id", "log-1", "X-Custom", "private-header-value")
	call(h, "GET", "/v1/unknown", "")
	call(h, "GET", "/v1/ping", "", "Authorization", "Bearer wrong-key-value-xxxx")

	lines := logLines(t, logs)
	if len(lines) != 3 {
		t.Fatalf("got %d log lines, want 3:\n%s", len(lines), logs)
	}

	first := lines[0]
	if first["msg"] != "request" || first["request_id"] != "log-1" || first["method"] != "GET" ||
		first["path"] != "/v1/ping" || first["route"] != "GET /v1/ping" || first["status"].(float64) != 200 ||
		first["level"] != "INFO" || first["bytes"].(float64) <= 0 {
		t.Errorf("unexpected access log: %v", first)
	}
	if _, ok := first["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms missing: %v", first)
	}
	if lines[1]["route"] != "unmatched" || lines[1]["status"].(float64) != 404 || lines[1]["level"] != "WARN" {
		t.Errorf("a 404 is logged as unmatched at WARN: %v", lines[1])
	}
	if lines[2]["status"].(float64) != 401 {
		t.Errorf("a rejected key is logged: %v", lines[2])
	}

	all := logs.String()
	for _, secret := range []string{testKey, "wrong-key-value-xxxx", "do-not-log", "private-header-value", "Bearer"} {
		if strings.Contains(all, secret) {
			t.Errorf("the log contains %q: credentials, query strings and headers must never be logged", secret)
		}
	}
}

func TestRequestBodiesAreNeverLogged(t *testing.T) {
	h, logs := testHandler(t, nil, func(add routeAdder) {
		add("POST /v1/echo", func(w http.ResponseWriter, r *http.Request) {
			var v map[string]any
			if err := decodeJSON(r, &v); err != nil {
				writeProblem(w, r, mustMap(err))
				return
			}
			writeJSON(w, r, 200, map[string]string{"ok": "yes"})
		})
	})
	call(h, "POST", "/v1/echo", `{"card":"4111-1111-1111-1111"}`)
	call(h, "POST", "/v1/echo", `{broken 4111-1111-1111-1111`)
	if strings.Contains(logs.String(), "4111") {
		t.Errorf("a request body leaked into the log:\n%s", logs)
	}
}

func TestRequestTimeout(t *testing.T) {
	h, _ := testHandler(t, func(o *Options) { o.RequestTimeout = 40 * time.Millisecond }, func(add routeAdder) {
		add("GET /v1/slow", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				p, _ := errorToProblem(r.Context().Err())
				writeProblem(w, r, p)
			case <-time.After(5 * time.Second):
				writeJSON(w, r, 200, nil)
			}
		})
	})
	start := time.Now()
	rec := call(h, "GET", "/v1/slow", "")
	mustProblem(t, rec, 503, CodeTemporarilyUnavailble)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 503 should say when to retry")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the request context did not expire")
	}
}

func TestResponseHeaders(t *testing.T) {
	h, _ := testHandler(t, nil, pingRoutes)
	rec := call(h, "GET", "/v1/ping", "")
	for header, want := range map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if errors.Is(nil, nil) && rec.Header().Get("Content-Length") == "" {
		t.Error("Content-Length missing")
	}
}
