package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestRun_Healthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("probed %s, want /readyz", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	env := map[string]string{"LEDGERD_ADDR": addr}

	if code, _, stderr := runCmd([]string{"healthcheck"}, env); code != exitOK {
		t.Errorf("a ready server: code=%d stderr=%q", code, stderr)
	}

	status = http.StatusServiceUnavailable
	if code, _, stderr := runCmd([]string{"healthcheck"}, env); code != exitFailure || !strings.Contains(stderr, "not ready (HTTP 503)") {
		t.Errorf("a server that is not ready: code=%d stderr=%q", code, stderr)
	}

	// Nothing is listening there any more.
	srv.Close()
	if code, _, _ := runCmd([]string{"healthcheck"}, env); code != exitFailure {
		t.Errorf("no server: code=%d", code)
	}

	if code, _, _ := runCmd([]string{"healthcheck"}, map[string]string{"LEDGERD_ADDR": "not-an-address"}); code != exitUsage {
		t.Errorf("a bad address: code=%d", code)
	}
}

func TestVersion_PrefersBuildTimeValues(t *testing.T) {
	buildVersion, buildCommit = "1.2.3", "0123456789abcdef0123"
	defer func() { buildVersion, buildCommit = "", "" }()
	if got := version(); got != "ledgerd 1.2.3 (0123456789ab)" {
		t.Errorf("version() = %q", got)
	}
}
