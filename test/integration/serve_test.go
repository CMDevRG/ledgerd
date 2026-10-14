package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/radadev/ledgerd/internal/app"
	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/testdb"
)

// liveServer is a ledgerd process-equivalent (app.Serve) listening on a real
// TCP port, backed by the test database.
type liveServer struct {
	base   string
	cancel context.CancelFunc
	done   chan error
	client *http.Client
}

func startServe(t *testing.T, database *testdb.DB, opts app.Options, tweak func(*config.ServeConfig)) *liveServer {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ServeConfig{
		Addr:           ln.Addr().String(),
		DatabaseURL:    config.Secret(database.AppDSN),
		APIKey:         config.Secret(apiKey),
		IdempotencyTTL: 48 * time.Hour,
		MaxBodyBytes:   1 << 20,
		DBMaxConns:     5,
	}
	if tweak != nil {
		tweak(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &liveServer{
		base:   "http://" + ln.Addr().String(),
		cancel: cancel,
		done:   make(chan error, 1),
		// No connection reuse: each request opens a connection, so a refused
		// connection really means the listener is closed.
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
	}
	go func() {
		s.done <- app.Serve(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ln, opts)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(20 * time.Second):
			t.Error("the server did not stop at the end of the test")
		}
	})
	return s
}

func (s *liveServer) get(t testing.TB, path string, authorised bool) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), "GET", s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorised {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close() //nolint:errcheck // test helper
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// accepting reports whether a new connection to the server succeeds.
func (s *liveServer) accepting() bool {
	req, err := http.NewRequestWithContext(context.Background(), "GET", s.base+"/healthz", nil)
	if err != nil {
		return false
	}
	res, err := s.client.Do(req)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return true
}

func (s *liveServer) post(path, body string, headers ...string) (int, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), "POST", s.base+path, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := s.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close() //nolint:errcheck // test helper
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), nil
}

func (s *liveServer) waitStopped(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-s.done:
		s.done <- err // keep it readable for the cleanup
		return err
	case <-time.After(within):
		t.Fatalf("Serve did not return within %v", within)
		return nil
	}
}

func TestServe_RunsAndStopsOnCancel(t *testing.T) {
	database := server.NewDB(t)
	s := startServe(t, database, app.Options{}, nil)

	if code, body := s.get(t, "/healthz", false); code != 200 || !strings.Contains(body, "ok") {
		t.Errorf("/healthz: %d %s", code, body)
	}
	if code, body := s.get(t, "/readyz", false); code != 200 || !strings.Contains(body, "ready") {
		t.Errorf("/readyz: %d %s", code, body)
	}
	if code, _ := s.get(t, "/v1/accounts/00000000-0000-0000-0000-000000000001", false); code != 401 {
		t.Errorf("an API call without a key: %d", code)
	}
	if code, _, err := s.post("/v1/accounts", `{"code":"live-1","currency":"USD","type":"ASSET","normal_balance":"DEBIT"}`); err != nil || code != 201 {
		t.Errorf("create account over a real socket: %d %v", code, err)
	}

	s.cancel()
	if err := s.waitStopped(t, 10*time.Second); err != nil {
		t.Errorf("a clean shutdown must return nil, got %v", err)
	}
	if s.accepting() {
		t.Error("the server still accepts connections after shutdown")
	}
}

// holdAccountLock keeps a row lock on an account in an open transaction, so a
// request that needs that account blocks until the lock is released.
func holdAccountLock(t *testing.T, database *testdb.DB, accountID string) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		t.Fatal(err)
	}
	var once bool
	release = func() {
		if !once {
			once = true
			_ = tx.Rollback(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

// waitForBlockedRequest polls until a ledgerd connection is waiting on a lock.
func waitForBlockedRequest(t *testing.T, database *testdb.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := database.Super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE application_name = 'ledgerd' AND wait_event_type = 'Lock' AND datname = current_database()`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no request ever blocked on the lock")
}

func TestServe_GracefulShutdownLetsInFlightRequestsFinish(t *testing.T) {
	a := newAPIEnv(t) // only to create accounts conveniently
	cash := a.createAccount(t, "cash", "USD", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "USD", "LIABILITY", "CREDIT", true)
	s := startServe(t, a.db, app.Options{ShutdownTimeout: 10 * time.Second}, nil)

	release := holdAccountLock(t, a.db, cash)

	type result struct {
		code int
		body string
		err  error
	}
	inFlight := make(chan result, 1)
	go func() {
		code, body, err := s.post("/v1/transactions", txBody(cash, wallet, "100", "USD"), "Idempotency-Key", "inflight-1")
		inFlight <- result{code, body, err}
	}()
	waitForBlockedRequest(t, a.db)

	// SIGTERM arrives while the request is in progress.
	s.cancel()
	time.Sleep(300 * time.Millisecond)

	select {
	case err := <-s.done:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	default:
	}
	if s.accepting() {
		t.Error("a new request was accepted after shutdown began")
	}

	release() // the in-flight request can now proceed
	res := <-inFlight
	if res.err != nil || res.code != 201 {
		t.Fatalf("the in-flight request must complete normally: %d %v %s", res.code, res.err, res.body)
	}
	if err := s.waitStopped(t, 10*time.Second); err != nil {
		t.Errorf("clean shutdown expected, got %v", err)
	}
	if n := a.count(t, "transactions"); n != 1 {
		t.Errorf("%d transactions, want the one that was in flight", n)
	}
}

func TestServe_ShutdownTimeoutCutsOffStuckRequests(t *testing.T) {
	a := newAPIEnv(t)
	cash := a.createAccount(t, "cash", "USD", "ASSET", "DEBIT", true)
	wallet := a.createAccount(t, "wallet", "USD", "LIABILITY", "CREDIT", true)
	s := startServe(t, a.db, app.Options{ShutdownTimeout: 300 * time.Millisecond}, nil)

	holdAccountLock(t, a.db, cash) // never released during the test
	stuck := make(chan error, 1)
	go func() {
		_, _, err := s.post("/v1/transactions", txBody(cash, wallet, "100", "USD"), "Idempotency-Key", "stuck-1")
		stuck <- err
	}()
	waitForBlockedRequest(t, a.db)

	start := time.Now()
	s.cancel()
	err := s.waitStopped(t, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "graceful shutdown did not finish") {
		t.Fatalf("want a shutdown-timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shutdown took %v with a 300ms limit", elapsed)
	}
	if clientErr := <-stuck; clientErr == nil {
		t.Error("the stuck request should have been cut off")
	}
	if n := a.count(t, "transactions"); n != 0 {
		t.Errorf("%d transactions committed by a cut-off request", n)
	}
}

func TestServe_RefusesToStartWithoutADatabase(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // test cleanup

	cfg := config.ServeConfig{
		Addr:        ln.Addr().String(),
		DatabaseURL: "postgres://ledgerd_app:pw-very-secret@127.0.0.1:1/ledgerd?sslmode=disable&connect_timeout=1",
		APIKey:      apiKey, MaxBodyBytes: 1 << 20, DBMaxConns: 2, IdempotencyTTL: 48 * time.Hour,
	}
	start := time.Now()
	err = app.Serve(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ln, app.Options{StartupTimeout: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("want a 'not reachable' error, got %v", err)
	}
	if strings.Contains(err.Error(), "pw-very-secret") {
		t.Errorf("the error leaks the database password: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("startup failure took too long")
	}

	cfg.DatabaseURL = "postgres://user:pw-very-secret@host:notaport/db"
	err = app.Serve(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ln, app.Options{})
	if err == nil || strings.Contains(err.Error(), "pw-very-secret") {
		t.Errorf("an unparsable URL must fail without echoing it: %v", err)
	}
}

func TestServe_PurgesExpiredIdempotencyKeysInTheBackground(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	if _, err := database.Super.Exec(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, response_status, response_body, created_at)
		 VALUES ('ancient', '\x00', 201, '{}', now() - interval '200 hours'),
		        ('recent',  '\x00', 201, '{}', now() - interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	startServe(t, database, app.Options{CleanupInterval: 50 * time.Millisecond}, nil)

	deadline := time.Now().Add(10 * time.Second)
	for {
		var keys []string
		rows, err := database.Super.Query(ctx, `SELECT key FROM idempotency_keys ORDER BY key`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k)
		}
		rows.Close()
		if len(keys) == 1 && keys[0] == "recent" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("keys left: %v, want only 'recent'", keys)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServe_ReadinessFollowsTheDatabase(t *testing.T) {
	a := newAPIEnv(t)
	if r := a.do(t, "GET", "/readyz", "", "Authorization", ""); r.Status != 200 {
		t.Fatalf("ready database: %d", r.Status)
	}

	// The database disappears: its connections are terminated and the data is gone.
	admin, err := pgx.Connect(context.Background(), server.SuperuserDSN("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background()) //nolint:errcheck // test cleanup
	if _, err := admin.Exec(context.Background(), `DROP DATABASE `+a.db.Name+` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	r := a.do(t, "GET", "/readyz", "", "Authorization", "")
	if r.Status != 503 || r.json(t)["code"] != "not_ready" {
		t.Errorf("readiness with a missing database: %d %s", r.Status, r.Body)
	}
	if strings.Contains(string(r.Body), a.db.Name) {
		t.Errorf("readiness leaked internal details: %s", r.Body)
	}
	if h := a.do(t, "GET", "/healthz", "", "Authorization", ""); h.Status != 200 {
		t.Errorf("liveness must stay up: %d", h.Status)
	}
}
