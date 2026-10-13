package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const (
	testDBURL  = "postgres://app:s3cr3t-db-pass@localhost:55432/ledgerd?sslmode=disable"
	testAPIKey = "test-api-key-0123456789"
)

func env(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func validServeEnv() map[string]string {
	return map[string]string{
		EnvDatabaseURL: testDBURL,
		EnvAPIKey:      testAPIKey,
	}
}

func TestLoadServe_Defaults(t *testing.T) {
	cfg, err := LoadServe(env(validServeEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if cfg.IdempotencyTTL != DefaultIdempotencyTTL {
		t.Errorf("IdempotencyTTL = %v, want %v", cfg.IdempotencyTTL, DefaultIdempotencyTTL)
	}
	if cfg.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, DefaultMaxBodyBytes)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.APIKey.Reveal() != testAPIKey || cfg.DatabaseURL.Reveal() != testDBURL {
		t.Error("secrets were not loaded verbatim")
	}
}

func TestLoadServe_Overrides(t *testing.T) {
	e := validServeEnv()
	e[EnvAddr] = "127.0.0.1:9000"
	e[EnvIdempotencyTTL] = "72h"
	e[EnvMaxBodyBytes] = "2048"
	e[EnvLogLevel] = "DEBUG"

	cfg, err := LoadServe(env(e))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.IdempotencyTTL != 72*time.Hour ||
		cfg.MaxBodyBytes != 2048 || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadServe_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string // substring that must appear in the error
	}{
		{"missing database url", func(m map[string]string) { delete(m, EnvDatabaseURL) }, EnvDatabaseURL},
		{"wrong scheme", func(m map[string]string) { m[EnvDatabaseURL] = "mysql://u:p@h/db" }, "scheme"},
		{"no host", func(m map[string]string) { m[EnvDatabaseURL] = "postgres:///db" }, "host"},
		{"missing api key", func(m map[string]string) { delete(m, EnvAPIKey) }, "required"},
		{"short api key", func(m map[string]string) { m[EnvAPIKey] = "short" }, "at least"},
		{"bad addr", func(m map[string]string) { m[EnvAddr] = "8085" }, EnvAddr},
		{"port out of range", func(m map[string]string) { m[EnvAddr] = ":99999" }, "port"},
		{"ttl not a duration", func(m map[string]string) { m[EnvIdempotencyTTL] = "two days" }, "duration"},
		{"ttl below minimum", func(m map[string]string) { m[EnvIdempotencyTTL] = "1h" }, "at least 24h"},
		{"body not integer", func(m map[string]string) { m[EnvMaxBodyBytes] = "big" }, "integer"},
		{"body too small", func(m map[string]string) { m[EnvMaxBodyBytes] = "10" }, "between"},
		{"body too large", func(m map[string]string) { m[EnvMaxBodyBytes] = "999999999999" }, "between"},
		{"bad log level", func(m map[string]string) { m[EnvLogLevel] = "loud" }, "one of"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validServeEnv()
			tc.mutate(e)
			_, err := LoadServe(env(e))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadServe_ReportsAllErrorsAtOnce(t *testing.T) {
	_, err := LoadServe(env(map[string]string{EnvLogLevel: "loud"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{EnvDatabaseURL, EnvAPIKey, EnvLogLevel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestLoadServe_ErrorsNeverLeakSecrets(t *testing.T) {
	// Every invalid input below contains a recognizable secret.
	cases := []map[string]string{
		{EnvDatabaseURL: "mysql://user:s3cr3t-db-pass@host/db", EnvAPIKey: "short-secret-key"},
		{EnvDatabaseURL: "not a url with s3cr3t-db-pass %zz", EnvAPIKey: ""},
		{EnvDatabaseURL: testDBURL, EnvAPIKey: "tiny-s3cr3t"},
	}
	for i, c := range cases {
		_, err := LoadServe(env(c))
		if err == nil {
			t.Fatalf("case %d: expected an error", i)
		}
		for _, secret := range []string{"s3cr3t-db-pass", "short-secret-key", "tiny-s3cr3t"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("case %d: error leaks secret %q: %v", i, secret, err)
			}
		}
	}
}

func TestSecret_NeverPrintsValue(t *testing.T) {
	cfg, err := LoadServe(env(validServeEnv()))
	if err != nil {
		t.Fatal(err)
	}

	outputs := map[string]string{
		"%v":  fmt.Sprintf("%v", cfg),
		"%+v": fmt.Sprintf("%+v", cfg),
		"%#v": fmt.Sprintf("%#v", cfg),
		"%s":  fmt.Sprintf("key=%s", cfg.APIKey),
	}

	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("config", "cfg", cfg, "key", cfg.APIKey)
	outputs["slog"] = logBuf.String()

	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outputs["json"] = string(jsonBytes)

	for name, out := range outputs {
		if strings.Contains(out, testAPIKey) || strings.Contains(out, "s3cr3t-db-pass") {
			t.Errorf("%s output leaks a secret: %s", name, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s output should contain %s: %s", name, redacted, out)
		}
	}
}

func TestLoadMigrate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		cfg, err := LoadMigrate(env(map[string]string{EnvMigrateDatabaseURL: testDBURL}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DatabaseURL.Reveal() != testDBURL {
			t.Error("database URL not loaded")
		}
	})
	t.Run("missing url", func(t *testing.T) {
		_, err := LoadMigrate(env(nil))
		if err == nil || !strings.Contains(err.Error(), EnvMigrateDatabaseURL) {
			t.Errorf("expected error mentioning %s, got %v", EnvMigrateDatabaseURL, err)
		}
	})
	t.Run("serve-only variables are not required", func(t *testing.T) {
		if _, err := LoadMigrate(env(map[string]string{EnvMigrateDatabaseURL: testDBURL})); err != nil {
			t.Errorf("migrate must not require API key: %v", err)
		}
	})
}
