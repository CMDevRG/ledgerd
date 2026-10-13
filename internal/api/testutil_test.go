package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/radadev/ledgerd/internal/config"
)

const testKey = "test-api-key-0123456789"

// testHandler builds the full middleware stack around the given routes and
// returns it with a buffer that receives the JSON log lines.
func testHandler(t *testing.T, tweak func(*Options), register func(add routeAdder)) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	o := Options{
		Logger:       slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		APIKey:       config.Secret(testKey),
		MaxBodyBytes: 1 << 10,
	}
	if tweak != nil {
		tweak(&o)
	}
	return build(o, register), logs
}

// call performs a request with the right API key unless overridden by headers.
func call(h http.Handler, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequestWithContext(context.Background(), method, target, nil)
	} else {
		req = httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mustProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) Problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != problemContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, problemContentType)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a problem document: %v\n%s", err, rec.Body)
	}
	if p.Code != code || p.Status != status || p.Type != problemTypePrefix+code || p.Title == "" {
		t.Fatalf("unexpected problem: %+v (want code %q)", p, code)
	}
	return p
}

// logLines parses the JSON log output.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func pingRoutes(add routeAdder) {
	add("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, map[string]string{"pong": "ok"})
	})
	add("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func slogDiscard(w *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
