package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func runCmd(args []string, env map[string]string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code = run(ctx, args, &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func TestRun_Usage(t *testing.T) {
	code, _, stderr := runCmd(nil, nil)
	if code != exitUsage || !strings.Contains(stderr, "Usage: ledgerd") {
		t.Errorf("no command: code=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runCmd([]string{"frobnicate"}, nil)
	if code != exitUsage || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("unknown command: code=%d stderr=%q", code, stderr)
	}

	for _, h := range []string{"help", "-h", "--help"} {
		code, stdout, _ := runCmd([]string{h}, nil)
		if code != exitOK || !strings.Contains(stdout, "serve") || !strings.Contains(stdout, "migrate") {
			t.Errorf("%s: code=%d stdout=%q", h, code, stdout)
		}
	}
}

func TestRun_Version(t *testing.T) {
	code, stdout, _ := runCmd([]string{"version"}, nil)
	if code != exitOK || !strings.HasPrefix(stdout, "ledgerd ") || !strings.Contains(stdout, "go1") {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
}

func TestRun_ServeRejectsBadConfigurationWithoutLeakingSecrets(t *testing.T) {
	env := map[string]string{
		"LEDGERD_DATABASE_URL": "mysql://user:db-password-123@host/db", // wrong scheme
		"LEDGERD_API_KEY":      "short-secret",                         // too short
	}
	code, _, stderr := runCmd([]string{"serve"}, env)
	if code != exitUsage {
		t.Fatalf("code = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"LEDGERD_DATABASE_URL", "LEDGERD_API_KEY"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should name %s: %q", want, stderr)
		}
	}
	for _, secret := range []string{"db-password-123", "short-secret"} {
		if strings.Contains(stderr, secret) {
			t.Errorf("stderr leaks %q: %q", secret, stderr)
		}
	}
}

func TestRun_MigrateNeedsItsOwnURL(t *testing.T) {
	// The serve URL (application role) must not be accepted for migrations.
	code, _, stderr := runCmd([]string{"migrate"}, map[string]string{"LEDGERD_DATABASE_URL": "postgres://app:pw@localhost/db"})
	if code != exitUsage || !strings.Contains(stderr, "LEDGERD_MIGRATE_DATABASE_URL") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestRun_ServeFailsCleanlyWhenThePortIsTaken(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close() //nolint:errcheck // test cleanup

	code, _, stderr := runCmd([]string{"serve"}, map[string]string{
		"LEDGERD_DATABASE_URL": "postgres://app:pw@localhost:1/db",
		"LEDGERD_API_KEY":      "test-api-key-0123456789",
		"LEDGERD_ADDR":         busy.Addr().String(),
	})
	if code != exitFailure || !strings.Contains(stderr, "cannot listen") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "pw@") {
		t.Errorf("credentials in output: %q", stderr)
	}
}
