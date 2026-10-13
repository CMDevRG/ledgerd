package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// writeJSON sends v as JSON. The body is encoded first, so an encoding failure
// produces a clean 500 instead of a truncated response. Responses carry
// financial data and are never cacheable.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeProblem(w, r, newProblem(CodeInternalError, "an unexpected error occurred"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// decodeJSON reads the request body into dst, strictly:
//   - the Content-Type must be application/json (UTF-8);
//   - unknown fields are errors, so a misspelled field cannot be silently ignored;
//   - exactly one JSON value must be present, with nothing after it.
//
// Failures are returned as *requestError values.
func decodeJSON(r *http.Request, dst any) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return &requestError{CodeUnsupportedMediaType, "Content-Type must be application/json"}
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return jsonError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return &requestError{CodeMalformedRequest, "the request body must contain a single JSON value"}
		}
		return jsonError(err)
	}
	return nil
}

func isJSONContentType(header string) bool {
	mt, params, err := mime.ParseMediaType(header)
	if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
		return false
	}
	cs, ok := params["charset"]
	return !ok || strings.EqualFold(cs, "utf-8")
}

// jsonError turns a decoding error into a client-safe requestError.
func jsonError(err error) error {
	var (
		maxBytes *http.MaxBytesError
		syntax   *json.SyntaxError
		typeErr  *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &maxBytes):
		return &requestError{CodePayloadTooLarge, "the request body exceeds the maximum size"}
	case errors.Is(err, io.EOF):
		return &requestError{CodeMalformedRequest, "the request body is required"}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &requestError{CodeMalformedRequest, "the request body is not complete JSON"}
	case errors.As(err, &syntax):
		return &requestError{CodeMalformedRequest, fmt.Sprintf("the request body is not valid JSON (at byte %d)", syntax.Offset)}
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		return &requestError{CodeMalformedRequest, fmt.Sprintf("field %q has the wrong type: expected %s, got JSON %s", field, typeErr.Type, typeErr.Value)}
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return &requestError{CodeMalformedRequest, strings.TrimPrefix(err.Error(), "json: ")}
	default:
		return &requestError{CodeMalformedRequest, "the request body could not be read"}
	}
}

// discardBody drains a (possibly empty) body that the handler does not use, so
// the connection can be reused, within the size limit already applied.
func discardBody(r *http.Request) error {
	_, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		return jsonError(err)
	}
	return nil
}
