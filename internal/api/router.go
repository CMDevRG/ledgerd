package api

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/radadev/ledgerd/internal/config"
)

// Options configures the HTTP handler.
type Options struct {
	Logger         *slog.Logger
	APIKey         config.Secret
	MaxBodyBytes   int64
	RequestTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = config.DefaultMaxBodyBytes
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 25 * time.Second
	}
	return o
}

// routeAdder registers a handler for a "METHOD /path" pattern.
type routeAdder func(pattern string, h http.HandlerFunc)

// build assembles the middleware stack around the routes that register adds.
// Everything the API shares (identification, logging, panic safety, timeouts,
// authentication, body limits, and JSON 404/405 answers) lives here, so each
// handler can concentrate on its own job.
func build(o Options, register func(add routeAdder)) http.Handler {
	o = o.withDefaults()

	mux := http.NewServeMux()
	add := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			recordRoute(r)
			h(w, r)
		})
	}
	register(add)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		recordRoute(r)
		unmatched(mux, w, r)
	})

	// Outermost first. The access log sits outside the panic handler so that a
	// recovered panic is logged with its real 500 status.
	return chain(mux,
		requestID(),
		accessLog(o.Logger),
		recoverer(o.Logger),
		withTimeout(o.RequestTimeout),
		authenticate(o.APIKey),
		limitBody(o.MaxBodyBytes),
	)
}

// unmatched answers requests no route handles: 405 with an Allow header when
// the path exists for other methods, 404 otherwise. Both as problem documents
// (the default mux would answer with plain text).
func unmatched(mux *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	var allowed []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/" {
			allowed = append(allowed, m)
		}
	}
	if len(allowed) > 0 {
		slices.Sort(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeProblem(w, r, newProblem(CodeMethodNotAllowed, "this method is not supported for "+r.URL.Path))
		return
	}
	writeProblem(w, r, newProblem(CodeNotFound, "no such endpoint: "+r.URL.Path))
}
