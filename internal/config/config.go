// Package config loads and validates service configuration from environment
// variables. Validation failures never include secret values.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvAddr               = "LEDGERD_ADDR"
	EnvDatabaseURL        = "LEDGERD_DATABASE_URL"
	EnvMigrateDatabaseURL = "LEDGERD_MIGRATE_DATABASE_URL"
	EnvAPIKey             = "LEDGERD_API_KEY" //nolint:gosec // name of an environment variable, not a credential
	EnvIdempotencyTTL     = "LEDGERD_IDEMPOTENCY_TTL"
	EnvMaxBodyBytes       = "LEDGERD_MAX_BODY_BYTES"
	EnvLogLevel           = "LEDGERD_LOG_LEVEL"
	EnvDBMaxConns         = "LEDGERD_DB_MAX_CONNS"
)

// Defaults and limits.
const (
	DefaultAddr           = ":8085"
	DefaultIdempotencyTTL = 48 * time.Hour
	MinIdempotencyTTL     = 24 * time.Hour // R6.7: keys are retained at least 24h
	DefaultMaxBodyBytes   = 1 << 20        // 1 MiB
	MinMaxBodyBytes       = 1 << 10        // 1 KiB
	MaxMaxBodyBytes       = 16 << 20       // 16 MiB
	MinAPIKeyLength       = 16
	DefaultDBMaxConns     = 20
	MaxDBMaxConns         = 500
)

// ServeConfig is the configuration of the `serve` command.
type ServeConfig struct {
	Addr           string
	DatabaseURL    Secret
	APIKey         Secret
	IdempotencyTTL time.Duration
	MaxBodyBytes   int64
	DBMaxConns     int32
	LogLevel       slog.Level
}

// MigrateConfig is the configuration of the `migrate` command. It uses a
// different (owner) database role than the running service.
type MigrateConfig struct {
	DatabaseURL Secret
	LogLevel    slog.Level
}

// Getenv looks up an environment variable (os.Getenv in production; a map
// lookup in tests, which avoids mutating the process environment).
type Getenv func(key string) string

// LoadServe reads and validates the configuration for the `serve` command.
func LoadServe(getenv Getenv) (ServeConfig, error) {
	var errs []error
	cfg := ServeConfig{}

	cfg.Addr = withDefault(getenv(EnvAddr), DefaultAddr)
	if err := validateAddr(cfg.Addr); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvAddr, err))
	}

	cfg.DatabaseURL = Secret(getenv(EnvDatabaseURL))
	if err := validateDatabaseURL(cfg.DatabaseURL.Reveal()); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvDatabaseURL, err))
	}

	cfg.APIKey = Secret(getenv(EnvAPIKey))
	switch {
	case cfg.APIKey == "":
		errs = append(errs, fmt.Errorf("%s: is required", EnvAPIKey))
	case len(cfg.APIKey) < MinAPIKeyLength:
		errs = append(errs, fmt.Errorf("%s: must be at least %d characters", EnvAPIKey, MinAPIKeyLength))
	}

	ttl, err := parseTTL(getenv(EnvIdempotencyTTL))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvIdempotencyTTL, err))
	}
	cfg.IdempotencyTTL = ttl

	maxBody, err := parseMaxBody(getenv(EnvMaxBodyBytes))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvMaxBodyBytes, err))
	}
	cfg.MaxBodyBytes = maxBody

	conns, err := parseDBMaxConns(getenv(EnvDBMaxConns))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvDBMaxConns, err))
	}
	cfg.DBMaxConns = conns

	level, err := parseLogLevel(getenv(EnvLogLevel))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogLevel, err))
	}
	cfg.LogLevel = level

	if len(errs) > 0 {
		return ServeConfig{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// LoadMigrate reads and validates the configuration for the `migrate` command.
func LoadMigrate(getenv Getenv) (MigrateConfig, error) {
	var errs []error
	cfg := MigrateConfig{DatabaseURL: Secret(getenv(EnvMigrateDatabaseURL))}

	if err := validateDatabaseURL(cfg.DatabaseURL.Reveal()); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvMigrateDatabaseURL, err))
	}

	level, err := parseLogLevel(getenv(EnvLogLevel))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogLevel, err))
	}
	cfg.LogLevel = level

	if len(errs) > 0 {
		return MigrateConfig{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

func withDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func validateAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("must be host:port or :port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// validateDatabaseURL checks the shape of the URL without ever echoing it,
// because it normally contains a password.
func validateDatabaseURL(raw string) error {
	if raw == "" {
		return errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return errors.New("must use the postgres:// or postgresql:// scheme")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	return nil
}

func parseTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultIdempotencyTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return DefaultIdempotencyTTL, errors.New("must be a Go duration such as 48h")
	}
	if d < MinIdempotencyTTL {
		return DefaultIdempotencyTTL, fmt.Errorf("must be at least %s", MinIdempotencyTTL)
	}
	return d, nil
}

func parseMaxBody(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMaxBodyBytes, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return DefaultMaxBodyBytes, errors.New("must be an integer number of bytes")
	}
	if n < MinMaxBodyBytes || n > MaxMaxBodyBytes {
		return DefaultMaxBodyBytes, fmt.Errorf("must be between %d and %d", MinMaxBodyBytes, MaxMaxBodyBytes)
	}
	return n, nil
}

func parseDBMaxConns(raw string) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultDBMaxConns, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return DefaultDBMaxConns, errors.New("must be an integer")
	}
	if n < 1 || n > MaxDBMaxConns {
		return DefaultDBMaxConns, fmt.Errorf("must be between 1 and %d", MaxDBMaxConns)
	}
	return int32(n), nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("must be one of debug, info, warn, error")
	}
}
