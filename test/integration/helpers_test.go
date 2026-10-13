package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Names of the PostgreSQL error classes the tests assert on.
const (
	codeInsufficientPrivilege = "42501"
	codeCheckViolation        = "23514"
	codeForeignKeyViolation   = "23503"
	codeUniqueViolation       = "23505"
)

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

var _ queryer = (*pgxpool.Pool)(nil)

// createAccount inserts an account and returns its id.
func createAccount(t *testing.T, db queryer, code, currency, normal string, allowNegative bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := db.QueryRow(context.Background(),
		`INSERT INTO accounts (code, currency, type, normal_balance, allow_negative)
		 VALUES ($1, $2, 'ASSET', $3, $4) RETURNING id`,
		code, currency, normal, allowNegative).Scan(&id)
	if err != nil {
		t.Fatalf("create account %s: %v", code, err)
	}
	return id
}

// line is one posting to insert.
type line struct {
	account  uuid.UUID
	dir      string // DEBIT or CREDIT
	amount   string // decimal digits
	currency string
}

// postTx inserts a transaction and its postings in one database transaction
// and returns the commit (or earlier) error. The deferred integrity triggers
// run at COMMIT, so violations of the balance rules are reported by it.
func postTx(ctx context.Context, pool *pgxpool.Pool, reverses *uuid.UUID, lines ...line) (uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var id uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO transactions (reverses_id, created_at) VALUES ($1, now()) RETURNING id`,
		reverses).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO postings (transaction_id, account_id, currency, direction, amount, balance_after, recorded_at)
			 VALUES ($1, $2, $3, $4, $5::text::numeric, 0, now())`,
			id, l.account, l.currency, l.dir, l.amount); err != nil {
			return uuid.Nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// mustPost is postTx for transactions that are expected to be accepted.
func mustPost(t *testing.T, pool *pgxpool.Pool, reverses *uuid.UUID, lines ...line) uuid.UUID {
	t.Helper()
	id, err := postTx(context.Background(), pool, reverses, lines...)
	if err != nil {
		t.Fatalf("transaction should have been accepted: %v", err)
	}
	return id
}

// requirePgError asserts that err is a PostgreSQL error with the given
// SQLSTATE, and (if non-empty) that its message or constraint name contains
// the given text.
func requirePgError(t *testing.T, err error, sqlstate, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLSTATE %s, but the statement succeeded", sqlstate)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a PostgreSQL error with SQLSTATE %s, got %T: %v", sqlstate, err, err)
	}
	if pgErr.Code != sqlstate {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, sqlstate)
	}
	if contains != "" && !strings.Contains(pgErr.Message+" "+pgErr.ConstraintName, contains) {
		t.Fatalf("error %q (constraint %q) does not contain %q", pgErr.Message, pgErr.ConstraintName, contains)
	}
}

// pair returns the two lines of a simple balanced transfer.
func pair(debit, credit uuid.UUID, amount, currency string) []line {
	return []line{
		{debit, "DEBIT", amount, currency},
		{credit, "CREDIT", amount, currency},
	}
}
