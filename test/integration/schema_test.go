package integration

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/radadev/ledgerd/internal/migrate"
	"github.com/radadev/ledgerd/internal/testdb"
)

func tableNames(t *testing.T, db *testdb.DB) []string {
	t.Helper()
	rows, err := db.Owner.Query(context.Background(),
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestSmoke_SchemaIsMigrated(t *testing.T) {
	db := server.NewDB(t)

	got := tableNames(t, db)
	for _, want := range []string{"accounts", "transactions", "postings", "idempotency_keys", "goose_db_version"} {
		if !slices.Contains(got, want) {
			t.Errorf("table %s missing; have %v", want, got)
		}
	}
}

func TestSmoke_BothRolesCanConnect(t *testing.T) {
	db := server.NewDB(t)
	for name, pool := range map[string]interface{ Ping(context.Context) error }{"owner": db.Owner, "app": db.App} {
		if err := pool.Ping(context.Background()); err != nil {
			t.Errorf("%s role cannot connect: %v", name, err)
		}
	}

	var user string
	if err := db.App.QueryRow(context.Background(), `SELECT current_user`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if user != testdb.AppRole {
		t.Errorf("app pool connected as %q, want %q", user, testdb.AppRole)
	}
}

func TestMigrations_RollBackAndReapply(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := migrate.DownAll(ctx, db.OwnerDSN); err != nil {
		t.Fatalf("rolling back: %v", err)
	}
	for _, n := range tableNames(t, db) {
		if n != "goose_db_version" {
			t.Errorf("table %s survived the rollback", n)
		}
	}

	if err := migrate.Up(ctx, db.OwnerDSN, log); err != nil {
		t.Fatalf("re-applying: %v", err)
	}
	if got := tableNames(t, db); !slices.Contains(got, "postings") {
		t.Errorf("schema not restored: %v", got)
	}

	// Running Up again is a no-op.
	if err := migrate.Up(ctx, db.OwnerDSN, log); err != nil {
		t.Fatalf("idempotent Up: %v", err)
	}
}
