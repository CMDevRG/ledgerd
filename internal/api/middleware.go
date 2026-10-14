package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/CMDevRG/ledgerd/internal/config"
)

const requestIDHeader = "X-Request-Id"

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxRouteHolder
)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// middleware wraps a handler.
type middleware func(http.Handler) http.Handler

// chain applies middlewares so that the first one is the outermost.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ---- request id ----

// Only a conservative alphabet is accepted from clients, so an id can be put in
// logs and headers without any risk of injecting line breaks or other control
// characters.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// requestID makes sure every request has an id: the client's X-Request-Id if
// it is well-formed, otherwise a fresh random one. The id is returned in the
// response header and attached to the context for logs and error documents.
func requestID() middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if !validRequestID.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(requestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
		})
	}
}

// ---- access log ----

// statusRecorder remembers the status and size of a response.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = http.StatusOK, true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// routeHolder lets the innermost handler tell the access log which route
// pattern matched (the mux only exposes it to the handler it dispatches to).
type routeHolder struct{ pattern string }

// accessLog writes one structured line per request: method, path, matched
// route, status, size and duration. It never logs headers, query strings or
// bodies, so API keys and payloads cannot end up in logs.
func accessLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			holder := &routeHolder{}
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), ctxRouteHolder, holder)))

			route := holder.pattern
			if route == "" || route == "/" {
				route = "unmatched"
			}
			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("request_id", requestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			)
		})
	}
}

// recordRoute is called by the handler registered for a route.
func recordRoute(r *http.Request) {
	if h, ok := r.Context().Value(ctxRouteHolder).(*routeHolder); ok {
		h.pattern = r.Pattern
	}
}

// ---- panic recovery ----

// recoverer turns a panic into a 500 problem response. The panic value and
// stack go to the log only; the client sees a generic message.
func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared by identity on purpose
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic while handling request",
					slog.String("request_id", requestIDFrom(r.Context())),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				writeProblem(w, r, newProblem(CodeInternalError, "an unexpected error occurred"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---- request timeout ----

// withTimeout bounds the time a request may use, so a slow database cannot tie
// up a connection forever. The context is passed down to every query.
func withTimeout(d time.Duration) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ---- body size ----

// limitBody rejects bodies larger than max. A declared Content-Length over the
// limit is refused at once; for chunked or understated bodies the reader fails
// as soon as the limit is crossed.
func limitBody(max int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				writeProblem(w, r, newProblem(CodePayloadTooLarge, "the request body exceeds the maximum size"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

// ---- authentication ----

// authenticate requires `Authorization: Bearer <api key>` on /v1 requests.
//
// Both the presented and the configured key are hashed with SHA-256 before the
// constant-time comparison. Hashing gives two values of equal length, so the
// comparison time reveals neither the key's content nor its length.
func authenticate(key config.Secret) middleware {
	want := sha256.Sum256([]byte(key.Reveal()))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
				next.ServeHTTP(w, r) // health endpoints and unknown paths outside /v1
				return
			}
			got := sha256.Sum256([]byte(bearerToken(r)))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				writeProblem(w, r, newProblem(CodeUnauthorized, "a valid API key is required: Authorization: Bearer <key>"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token of an "Authorization: Bearer <token>" header,
// or "" if the header is missing or in another form. The scheme is
// case-insensitive (RFC 9110); there must be exactly one space and no padding.
func bearerToken(r *http.Request) string {
	const scheme = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return ""
	}
	return h[len(scheme):]
}
