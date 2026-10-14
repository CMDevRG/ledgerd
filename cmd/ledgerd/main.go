// Command ledgerd is a double-entry accounting ledger service.
//
//	ledgerd serve     run the HTTP API
//	ledgerd migrate   apply database migrations (uses the owner role)
//	ledgerd version   print version information
//	ledgerd healthcheck   probe /readyz of a running instance (for container health checks)
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/radadev/ledgerd/internal/app"
	"github.com/radadev/ledgerd/internal/config"
	"github.com/radadev/ledgerd/internal/migrate"
)

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1 // the command ran and failed
	exitUsage   = 2 // bad command line or configuration
)

const usage = `Usage: ledgerd <command>

Commands:
  serve     Run the HTTP API
  migrate   Apply database schema migrations
  version   Print version information
  healthcheck   Probe /readyz of a running instance (used by container health checks)

Configuration is read from environment variables (see README).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// run is main without the process-level side effects, so it can be tested.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) == 0 {
		say(stderr, "%s", usage)
		return exitUsage
	}
	switch args[0] {
	case "serve":
		return serve(ctx, stderr, getenv)
	case "migrate":
		return runMigrate(ctx, stderr, getenv)
	case "healthcheck":
		return healthcheck(ctx, stderr, getenv)
	case "version":
		say(stdout, "%s\n", version())
		return exitOK
	case "help", "-h", "--help":
		say(stdout, "%s", usage)
		return exitOK
	default:
		say(stderr, "ledgerd: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// say writes to a stream the program cannot do anything about if it is broken
// (stdout/stderr), so a write error is deliberately ignored.
func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

func serve(ctx context.Context, stderr io.Writer, getenv config.Getenv) int {
	cfg, err := config.LoadServe(getenv)
	if err != nil {
		say(stderr, "ledgerd: %v\n", err)
		return exitUsage
	}
	log := newLogger(stderr, cfg.LogLevel)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		log.Error("cannot listen", slog.String("addr", cfg.Addr), slog.Any("error", err))
		return exitFailure
	}
	if err := app.Serve(ctx, cfg, log, ln, app.Options{}); err != nil {
		log.Error("ledgerd stopped with an error", slog.Any("error", err))
		return exitFailure
	}
	return exitOK
}

func runMigrate(ctx context.Context, stderr io.Writer, getenv config.Getenv) int {
	cfg, err := config.LoadMigrate(getenv)
	if err != nil {
		say(stderr, "ledgerd: %v\n", err)
		return exitUsage
	}
	log := newLogger(stderr, cfg.LogLevel)
	if err := migrate.Up(ctx, cfg.DatabaseURL.Reveal(), log); err != nil {
		log.Error("migration failed", slog.Any("error", err))
		return exitFailure
	}
	return exitOK
}

// healthcheck asks a running instance whether it is ready and exits 0 if so.
// Container images such as distroless have no shell or curl, so the binary
// carries its own probe. It targets the address in LEDGERD_ADDR on the loopback
// interface.
func healthcheck(ctx context.Context, stderr io.Writer, getenv config.Getenv) int {
	addr := getenv(config.EnvAddr)
	if addr == "" {
		addr = config.DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		say(stderr, "healthcheck: invalid %s: %v\n", config.EnvAddr, err)
		return exitUsage
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1" // listening on all interfaces; probe the loopback one
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		say(stderr, "healthcheck: %v\n", err)
		return exitFailure
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		say(stderr, "healthcheck: %v\n", err)
		return exitFailure
	}
	defer res.Body.Close() //nolint:errcheck // nothing useful to do on a failed close
	if res.StatusCode != http.StatusOK {
		say(stderr, "healthcheck: not ready (HTTP %d)\n", res.StatusCode)
		return exitFailure
	}
	return exitOK
}

// Set at build time with -ldflags "-X main.buildVersion=... -X main.buildCommit=...".
// They matter for container builds, where the git directory is not available
// for the Go toolchain to read.
var (
	buildVersion string
	buildCommit  string
)

// version reports the module version and, when built from a git checkout, the
// commit it was built from.
func version() string {
	if buildVersion != "" {
		commit := buildCommit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if commit != "" {
			return fmt.Sprintf("ledgerd %s (%s)", buildVersion, commit)
		}
		return "ledgerd " + buildVersion
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "ledgerd (unknown build)"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		v = "devel"
	}
	rev, dirty := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		return fmt.Sprintf("ledgerd %s (%s%s) %s", v, rev, dirty, info.GoVersion)
	}
	return fmt.Sprintf("ledgerd %s %s", v, info.GoVersion)
}
