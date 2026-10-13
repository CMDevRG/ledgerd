package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(body, contentType string) error {
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	var dst struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	return decodeJSON(req, &dst)
}

func TestDecodeJSON(t *testing.T) {
	if err := decodeBody(`{"name":"a","n":1}`, "application/json"); err != nil {
		t.Fatalf("valid body: %v", err)
	}
	for _, ct := range []string{"application/json; charset=utf-8", "APPLICATION/JSON", "application/vnd.api+json"} {
		if err := decodeBody(`{}`, ct); err != nil {
			t.Errorf("content type %q should be accepted: %v", ct, err)
		}
	}

	tests := []struct {
		name, body, contentType, code string
	}{
		{"no content type", `{}`, "", CodeUnsupportedMediaType},
		{"form content type", `{}`, "application/x-www-form-urlencoded", CodeUnsupportedMediaType},
		{"text content type", `{}`, "text/plain", CodeUnsupportedMediaType},
		{"non-UTF-8 charset", `{}`, "application/json; charset=latin1", CodeUnsupportedMediaType},
		{"garbage content type", `{}`, "///", CodeUnsupportedMediaType},
		{"empty body", ``, "application/json", CodeMalformedRequest},
		{"only whitespace", `   `, "application/json", CodeMalformedRequest},
		{"syntax error", `{"name": }`, "application/json", CodeMalformedRequest},
		{"truncated", `{"name":"a"`, "application/json", CodeMalformedRequest},
		{"unknown field", `{"name":"a","nam":"typo"}`, "application/json", CodeMalformedRequest},
		{"wrong type", `{"name":5}`, "application/json", CodeMalformedRequest},
		{"array instead of object", `[]`, "application/json", CodeMalformedRequest},
		{"trailing value", `{"name":"a"} {"name":"b"}`, "application/json", CodeMalformedRequest},
		{"trailing garbage", `{"name":"a"} xyz`, "application/json", CodeMalformedRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeBody(tc.body, tc.contentType)
			var re *requestError
			if !errors.As(err, &re) || re.code != tc.code {
				t.Fatalf("got %v, want a requestError with code %s", err, tc.code)
			}
			if re.detail == "" {
				t.Error("the detail should explain the problem")
			}
		})
	}
}

func TestDecodeJSON_UnknownFieldIsNamed(t *testing.T) {
	err := decodeBody(`{"nam":"typo"}`, "application/json")
	var re *requestError
	if !errors.As(err, &re) || !strings.Contains(re.detail, `"nam"`) {
		t.Errorf("the detail should name the unknown field: %v", err)
	}
}

func TestDecodeJSON_WrongTypeNamesTheField(t *testing.T) {
	err := decodeBody(`{"name":5}`, "application/json")
	var re *requestError
	if !errors.As(err, &re) || !strings.Contains(re.detail, `"name"`) || !strings.Contains(re.detail, "string") {
		t.Errorf("the detail should name the field and expected type: %v", err)
	}
}

func TestDecodeJSON_BodyTooLarge(t *testing.T) {
	body := `{"name":"` + strings.Repeat("x", 100) + `"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, 20)

	var dst map[string]any
	err := decodeJSON(req, &dst)
	var re *requestError
	if !errors.As(err, &re) || re.code != CodePayloadTooLarge {
		t.Fatalf("got %v, want payload_too_large", err)
	}
}

func TestDecodeJSON_ErrorsDoNotEchoTheBody(t *testing.T) {
	err := decodeBody(`{"name":"super-secret-value", broken`, "application/json")
	if err == nil || strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("error text must not contain request data: %v", err)
	}
}

func TestDiscardBody(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/", strings.NewReader("anything"))
	if err := discardBody(req); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/", io.NopCloser(strings.NewReader(strings.Repeat("x", 100))))
	req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, 10)
	var re *requestError
	if err := discardBody(req); !errors.As(err, &re) || re.code != CodePayloadTooLarge {
		t.Errorf("a body over the limit must be reported even when ignored: %v", err)
	}
}
