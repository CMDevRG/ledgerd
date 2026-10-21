package deploy_test

import (
	"os"
	"regexp"
	"testing"
)

// On the first start the PostgreSQL image runs its init scripts (here: creating the
// ledgerd roles) on a temporary server that listens only on a Unix socket, stops it, and
// starts the real server on TCP. A health check that asks over the socket (the default of
// pg_isready) passes during the first phase; the migration then starts, connects over TCP
// and is refused, and the stack fails to come up about one start in six. The check must ask
// over TCP so that it passes only when the real server is listening. This was found by
// ledgerd-console's compose file and measured here: 2 failed starts in 12 before the fix.
func TestPostgresHealthcheckAsksOverTCP(t *testing.T) {
	text, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	checks := regexp.MustCompile(`(?m)^\s*test:.*pg_isready.*$`).FindAllString(string(text), -1)
	if len(checks) != 1 {
		t.Fatalf("expected exactly one pg_isready health check in docker-compose.yml, found %d", len(checks))
	}
	if !regexp.MustCompile(`pg_isready\s+(?:\S+\s+)*-h\s+(?:127\.0\.0\.1|localhost|::1)\b`).MatchString(checks[0]) {
		t.Fatalf("the PostgreSQL health check must use TCP (pg_isready -h 127.0.0.1 ...), not the Unix socket:\n%s", checks[0])
	}
}
