// Package app wires the service together and runs it: database pool, store,
// use cases, HTTP server, background cleanup, and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/radadev/ledgerd/internal/api"
	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/reconcile"
	"github.com/radadev/ledgerd/internal/service"
	"github.com/radadev/ledgerd/internal/store"
)

// Options tunes behaviour that is not configuration from the environment.
type Options struct {
	// ShutdownTimeout is how long in-flight requests get to finish after a
	// shutdown signal. Default 15s.
	ShutdownTimeout time.Duration
	// StartupTimeout bounds the first database connection. Default 10s.
	StartupTimeout time.Duration
	// CleanupInterval is how often expired idempotency keys are purged. Default 10m.
	CleanupInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 15 * time.Second
	}
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = 10 * time.Second
	}
	if o.CleanupInterval <= 0 {
		o.CleanupInterval = 10 * time.Minute
	}
	return o
}

// Serve runs the HTTP service on ln until ctx is cancelled (typically by
// SIGINT/SIGTERM), then shuts down gracefully:
//
//  1. stop accepting new connections and requests;
//  2. let requests already in progress finish, up to Options.ShutdownTimeout;
//  3. stop the background cleanup;
//  4. close the database pool.
//
// It returns nil after a clean shutdown. If in-flight requests do not finish
// in time they are cut off and an error is returned.
func Serve(ctx context.Context, cfg config.ServeConfig, log *slog.Logger, ln net.Listener, opts Options) error {
	opts = opts.withDefaults()

	pool, err := openPool(ctx, cfg, opts.StartupTimeout)
	if err != nil {
		return err
	}
	defer pool.Close()

	st := store.New(pool, store.Config{})
	svc := service.New(st)

	handler := api.New(api.Options{
		Logger:       log,
		APIKey:       cfg.APIKey,
		MaxBodyBytes: cfg.MaxBodyBytes,
	}, api.Deps{
		Ledger: svc,
		Reconcile: func(ctx context.Context, deep bool) (ledger.ReconciliationReport, error) {
			return reconcile.Run(ctx, st, reconcile.Options{Deep: deep})
		},
		Ready: pool.Ping,
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      35 * time.Second, // longer than the 25s request timeout
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	// Background work stops when the server stops.
	bgCtx, stopBackground := context.WithCancel(context.WithoutCancel(ctx))
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		svc.RunIdempotencyCleanup(bgCtx, cfg.IdempotencyTTL, opts.CleanupInterval, log)
	}()
	defer func() {
		stopBackground()
		bg.Wait()
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	// Secrets are Secret values and print as [REDACTED]; durations are logged as durations.
	log.Info("ledgerd listening",
		slog.String("addr", ln.Addr().String()),
		slog.Any("database_url", cfg.DatabaseURL),
		slog.Any("api_key", cfg.APIKey),
		slog.Duration("idempotency_ttl", cfg.IdempotencyTTL),
		slog.Int64("max_body_bytes", cfg.MaxBodyBytes),
		slog.Int("db_max_conns", int(cfg.DBMaxConns)),
		slog.String("log_level", cfg.LogLevel.String()))

	select {
	case err := <-serveErr: // the listener failed before any shutdown was requested
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down: no new requests are accepted, finishing in-flight ones",
		slog.Duration("timeout", opts.ShutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close() // cut off whatever is still running
		return fmt.Errorf("graceful shutdown did not finish within %s: %w", opts.ShutdownTimeout, err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	log.Info("shutdown complete")
	return nil
}

func openPool(ctx context.Context, cfg config.ServeConfig, startup time.Duration) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL.Reveal())
	if err != nil {
		// pgx's message can quote parts of the URL; keep credentials out of our errors.
		return nil, errors.New("the database URL could not be parsed")
	}
	pcfg.MaxConns = cfg.DBMaxConns
	if pcfg.ConnConfig.RuntimeParams == nil {
		pcfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	pcfg.ConnConfig.RuntimeParams["application_name"] = "ledgerd"

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, startup)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("the database is not reachable: %w", err)
	}
	return pool, nil
}
