// Package testdb starts a real PostgreSQL server in Docker for tests and hands
// out isolated, fully migrated databases.
//
// It is meant to be used from TestMain:
//
//	var server *testdb.Server
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Main(m, &server)) }
//
// One container is started per test package. Each test gets its own database,
// cloned from a template that already has all migrations applied, which takes
// a few milliseconds instead of re-running the migrations.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/CMDevRG/ledgerd/internal/migrate"
)

const (
	// Image is pinned by tag and digest so test runs are reproducible.
	Image = "postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea"

	// Fixed credentials: the container is throw-away and listens only on a
	// random localhost port.
	superUser     = "postgres"
	superPassword = "test-superuser-pw"
	OwnerRole     = "ledgerd_owner"
	OwnerPassword = "test-owner-pw"
	AppRole       = "ledgerd_app"
	AppPassword   = "test-app-pw"

	bootstrapDB  = "ledgerd"
	templateName = "ledgerd_template"

	// RequireDockerEnv makes the tests fail (instead of skip) when Docker is
	// unavailable. Continuous integration sets it.
	RequireDockerEnv = "LEDGERD_REQUIRE_DOCKER"
)

// ErrDockerUnavailable is returned by Start when no Docker daemon is reachable.
var ErrDockerUnavailable = errors.New("docker is not available")

// Server is a running PostgreSQL container with the ledgerd roles and a
// migrated template database.
type Server struct {
	container *tcpostgres.PostgresContainer
	host      string
	port      string
	counter   atomic.Int64
}

// Start launches the container, creates the roles with the same script that
// docker-compose uses, and prepares the migrated template database.
func Start(ctx context.Context) (*Server, error) {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDockerUnavailable, err)
	}
	defer provider.Close() //nolint:errcheck // best effort
	if err := provider.Health(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDockerUnavailable, err)
	}

	script, err := rolesScript()
	if err != nil {
		return nil, err
	}

	ctr, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase(bootstrapDB),
		tcpostgres.WithUsername(superUser),
		tcpostgres.WithPassword(superPassword),
		tcpostgres.WithInitScripts(script),
		testcontainers.WithEnv(map[string]string{
			"LEDGERD_OWNER_PASSWORD": OwnerPassword,
			"LEDGERD_APP_PASSWORD":   AppPassword,
		}),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	s := &Server{container: ctr}
	host, err := ctr.Host(ctx)
	if err != nil {
		_ = s.Stop(ctx)
		return nil, fmt.Errorf("container host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = s.Stop(ctx)
		return nil, fmt.Errorf("container port: %w", err)
	}
	s.host, s.port = host, port.Port()

	if err := s.createTemplate(ctx); err != nil {
		_ = s.Stop(ctx)
		return nil, err
	}
	return s, nil
}

// Stop terminates the container.
func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.container == nil {
		return nil
	}
	return s.container.Terminate(ctx)
}

func rolesScript() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate testdb source file")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "deploy", "initdb", "01-roles.sh")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("roles script: %w", err)
	}
	return path, nil
}

func (s *Server) dsn(user, password, db string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, s.host, s.port, db)
}

// SuperuserDSN connects as the bootstrap superuser (for test setup only).
func (s *Server) SuperuserDSN(db string) string { return s.dsn(superUser, superPassword, db) }

func (s *Server) createTemplate(ctx context.Context) error {
	admin, err := pgx.Connect(ctx, s.SuperuserDSN("postgres"))
	if err != nil {
		return fmt.Errorf("connect as superuser: %w", err)
	}
	defer admin.Close(ctx) //nolint:errcheck // best effort

	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, templateName, OwnerRole)); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := migrate.Up(ctx, s.dsn(OwnerRole, OwnerPassword, templateName), log); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

// DB is an isolated database for one test.
type DB struct {
	Name string
	// OwnerDSN and AppDSN connect as ledgerd_owner and ledgerd_app.
	OwnerDSN string
	AppDSN   string
	// Owner and App are connection pools for the two roles.
	Owner *pgxpool.Pool
	App   *pgxpool.Pool
	// Super is a pool for the superuser (to inspect or corrupt data on purpose).
	Super *pgxpool.Pool
}

// NewDB creates an empty, fully migrated database owned by ledgerd_owner and
// registers its removal with t.Cleanup.
func (s *Server) NewDB(t testing.TB) *DB {
	t.Helper()
	ctx := context.Background()

	name := fmt.Sprintf("ledgerd_test_%d_%d", time.Now().UnixNano(), s.counter.Add(1))

	admin, err := pgx.Connect(ctx, s.SuperuserDSN("postgres"))
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	defer admin.Close(ctx) //nolint:errcheck // best effort

	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s OWNER %s`, name, templateName, OwnerRole)); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}

	db := &DB{
		Name:     name,
		OwnerDSN: s.dsn(OwnerRole, OwnerPassword, name),
		AppDSN:   s.dsn(AppRole, AppPassword, name),
	}
	for _, p := range []struct {
		dst **pgxpool.Pool
		dsn string
	}{{&db.Owner, db.OwnerDSN}, {&db.App, db.AppDSN}, {&db.Super, s.SuperuserDSN(name)}} {
		pool, err := pgxpool.New(ctx, p.dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		*p.dst = pool
	}

	t.Cleanup(func() {
		db.Owner.Close()
		db.App.Close()
		db.Super.Close()
		cleanup, err := pgx.Connect(ctx, s.SuperuserDSN("postgres"))
		if err != nil {
			return
		}
		defer cleanup.Close(ctx) //nolint:errcheck // best effort
		_, _ = cleanup.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
	})
	return db
}

// Main is a TestMain helper: it starts the server, runs the tests, stops the
// server and returns the exit code. If Docker is unavailable the tests are
// skipped, unless LEDGERD_REQUIRE_DOCKER is set, in which case they fail.
func Main(m *testing.M, target **Server) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	srv, err := Start(ctx)
	cancel()
	if err != nil {
		if errors.Is(err, ErrDockerUnavailable) && os.Getenv(RequireDockerEnv) == "" {
			fmt.Fprintf(os.Stderr, "SKIP: integration tests need Docker (%v)\n", err)
			return 0
		}
		fmt.Fprintf(os.Stderr, "FAIL: cannot start test database: %v\n", err)
		return 1
	}
	*target = srv

	code := m.Run()

	stopCtx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	if err := srv.Stop(stopCtx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: stopping test database: %v\n", err)
	}
	return code
}
