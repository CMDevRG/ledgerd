// Package integration holds tests that run against a real PostgreSQL server
// started in Docker (see internal/testdb).
package integration

import (
	"os"
	"testing"

	"github.com/radadev/ledgerd/internal/testdb"
)

var server *testdb.Server

func TestMain(m *testing.M) {
	os.Exit(testdb.Main(m, &server))
}
