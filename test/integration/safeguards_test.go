package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These tests prove that each guarantee is enforced by PostgreSQL itself,
// independently of any application code: they use plain SQL as the
// least-privilege application role (and, where it matters, as the table owner
// and as a superuser).

type accounts struct{ cash, wallet, usd, eur uuid.UUID }

// seed creates a debit-normal cash account and a credit-normal wallet in USD,
// plus an EUR account used for currency-mismatch tests.
func seed(t *testing.T, db queryer) accounts {
	t.Helper()
	return accounts{
		cash:   createAccount(t, db, "bank-cash", "USD", "DEBIT", false),
		wallet: createAccount(t, db, "user-wallet", "USD", "CREDIT", false),
		usd:    createAccount(t, db, "usd-extra", "USD", "DEBIT", true),
		eur:    createAccount(t, db, "eur-cash", "EUR", "DEBIT", true),
	}
}

func TestAppendOnly(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)
	txID := mustPost(t, db.App, nil, pair(a.cash, a.wallet, "100", "USD")...)

	statements := []struct{ name, sql string }{
		{"update posting amount", `UPDATE postings SET amount = 1`},
		{"update posting account", `UPDATE postings SET account_id = account_id`},
		{"update transaction description", `UPDATE transactions SET description = 'tampered'`},
		{"delete posting", `DELETE FROM postings`},
		{"delete transaction", `DELETE FROM transactions`},
		{"truncate postings", `TRUNCATE postings`},
		{"truncate transactions with cascade", `TRUNCATE transactions CASCADE`},
		{"truncate every ledger table", `TRUNCATE transactions, postings, idempotency_keys`},
	}

	t.Run("application role is refused by privileges", func(t *testing.T) {
		for _, s := range statements {
			_, err := db.App.Exec(ctx, s.sql)
			requirePgError(t, err, codeInsufficientPrivilege, "")
		}
	})

	t.Run("table owner is refused by the append-only triggers", func(t *testing.T) {
		for _, s := range statements {
			t.Run(s.name, func(t *testing.T) {
				_, err := db.Owner.Exec(ctx, s.sql)
				requirePgError(t, err, codeInsufficientPrivilege, "append-only")
			})
		}
	})

	t.Run("even a superuser is refused", func(t *testing.T) {
		for _, s := range statements {
			t.Run(s.name, func(t *testing.T) {
				_, err := db.Super.Exec(ctx, s.sql)
				requirePgError(t, err, codeInsufficientPrivilege, "append-only")
			})
		}
	})

	t.Run("plain TRUNCATE of transactions is also stopped by its foreign keys", func(t *testing.T) {
		_, err := db.Owner.Exec(ctx, `TRUNCATE transactions`)
		requirePgError(t, err, "0A000", "referenced in a foreign key")
	})

	var n int
	if err := db.Super.QueryRow(ctx, `SELECT count(*) FROM postings WHERE transaction_id = $1`, txID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("history was altered: %d postings remain, want 2", n)
	}
}

func TestBalancedTransactions(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	t.Run("balanced transaction commits", func(t *testing.T) {
		mustPost(t, db.App, nil, pair(a.cash, a.wallet, "100", "USD")...)
	})

	t.Run("one debit split over two credits commits", func(t *testing.T) {
		mustPost(t, db.App, nil,
			line{a.cash, "DEBIT", "100", "USD"},
			line{a.wallet, "CREDIT", "60", "USD"},
			line{a.usd, "CREDIT", "40", "USD"})
	})

	t.Run("two currencies, each balanced, commits", func(t *testing.T) {
		mustPost(t, db.App, nil,
			line{a.cash, "DEBIT", "10", "USD"}, line{a.wallet, "CREDIT", "10", "USD"},
			line{a.eur, "DEBIT", "7", "EUR"}, line{a.eur, "CREDIT", "7", "EUR"})
	})

	rejected := []struct {
		name  string
		lines []line
	}{
		{"debits exceed credits by one", []line{
			{a.cash, "DEBIT", "100", "USD"}, {a.wallet, "CREDIT", "99", "USD"}}},
		{"credits exceed debits", []line{
			{a.cash, "DEBIT", "1", "USD"}, {a.wallet, "CREDIT", "2", "USD"}}},
		{"only debits", []line{
			{a.cash, "DEBIT", "5", "USD"}, {a.usd, "DEBIT", "5", "USD"}}},
		{"huge off-by-one", []line{
			{a.cash, "DEBIT", "1000000000000000000000000000000", "USD"},
			{a.wallet, "CREDIT", "999999999999999999999999999999", "USD"}}},
		{"one currency balanced, the other not", []line{
			{a.cash, "DEBIT", "10", "USD"}, {a.wallet, "CREDIT", "10", "USD"},
			{a.eur, "DEBIT", "7", "EUR"}, {a.eur, "CREDIT", "6", "EUR"}}},
		{"single posting", []line{{a.cash, "DEBIT", "5", "USD"}}},
		{"no postings at all", nil},
	}
	for _, tc := range rejected {
		t.Run("rejected: "+tc.name, func(t *testing.T) {
			_, err := postTx(ctx, db.App, nil, tc.lines...)
			requirePgError(t, err, codeCheckViolation, "")
			if !strings.Contains(err.Error(), "balanced") && !strings.Contains(err.Error(), "at least 2 postings") {
				t.Errorf("unexpected message: %v", err)
			}
		})
	}

	t.Run("rejected: adding a posting to an already committed transaction", func(t *testing.T) {
		// The original transaction is balanced and committed. A later database
		// transaction that inserts one more posting into it would unbalance it.
		id := mustPost(t, db.App, nil, pair(a.cash, a.wallet, "50", "USD")...)
		tx, err := db.App.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after a failed commit is a no-op
		if _, err := tx.Exec(ctx,
			`INSERT INTO postings (transaction_id, account_id, currency, direction, amount, balance_after, recorded_at)
			 VALUES ($1, $2, 'USD', 'DEBIT', 1, 0, now())`, id, a.cash); err != nil {
			t.Fatal(err)
		}
		requirePgError(t, tx.Commit(ctx), codeCheckViolation, "not balanced")
	})

	// A rejected transaction leaves nothing behind.
	var n int
	if err := db.Super.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("%d transactions persisted, want only the 4 accepted ones", n)
	}
}

func TestNonNegativeBalance(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	t.Run("protected account cannot go negative", func(t *testing.T) {
		_, err := db.App.Exec(ctx, `UPDATE accounts SET balance = -1 WHERE id = $1`, a.cash)
		requirePgError(t, err, codeCheckViolation, "accounts_non_negative")
	})
	t.Run("zero is allowed", func(t *testing.T) {
		if _, err := db.App.Exec(ctx, `UPDATE accounts SET balance = 0 WHERE id = $1`, a.cash); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("account with allow_negative may go negative", func(t *testing.T) {
		if _, err := db.App.Exec(ctx, `UPDATE accounts SET balance = -5 WHERE id = $1`, a.usd); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a new protected account cannot start negative", func(t *testing.T) {
		_, err := db.App.Exec(ctx,
			`INSERT INTO accounts (code, currency, type, normal_balance, allow_negative, balance)
			 VALUES ('neg', 'USD', 'ASSET', 'DEBIT', false, -1)`)
		requirePgError(t, err, codeCheckViolation, "accounts_non_negative")
	})
}

func TestCurrencyMustMatchAccount(t *testing.T) {
	db := server.NewDB(t)
	a := seed(t, db.App)

	// The wallet is in USD; a posting claiming EUR must be refused.
	_, err := postTx(context.Background(), db.App, nil,
		line{a.eur, "DEBIT", "10", "EUR"},
		line{a.wallet, "CREDIT", "10", "EUR"})
	requirePgError(t, err, codeForeignKeyViolation, "postings_account_currency_fkey")
}

func TestSingleReversal(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	original := mustPost(t, db.App, nil, pair(a.cash, a.wallet, "100", "USD")...)
	mustPost(t, db.App, &original, pair(a.wallet, a.cash, "100", "USD")...) // first reversal: fine

	_, err := postTx(ctx, db.App, &original, pair(a.wallet, a.cash, "100", "USD")...)
	requirePgError(t, err, codeUniqueViolation, "transactions_reverses_id_key")

	t.Run("a transaction cannot reverse itself", func(t *testing.T) {
		_, err := db.Owner.Exec(ctx, `INSERT INTO transactions (id, reverses_id, created_at)
			VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', now())`)
		requirePgError(t, err, codeCheckViolation, "transactions_not_self_reversing")
	})
}

func TestAmountAndFormatConstraints(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	t.Run("posting amounts", func(t *testing.T) {
		for _, bad := range []string{"0", "-1", "1000000000000000000000000000001"} {
			_, err := postTx(ctx, db.App, nil,
				line{a.cash, "DEBIT", bad, "USD"}, line{a.wallet, "CREDIT", bad, "USD"})
			requirePgError(t, err, codeCheckViolation, "postings_amount_range")
		}
		// Exactly 10^30 is the largest allowed amount.
		mustPost(t, db.App, nil, pair(a.cash, a.wallet, "1000000000000000000000000000000", "USD")...)
	})

	t.Run("posting direction", func(t *testing.T) {
		_, err := postTx(ctx, db.App, nil,
			line{a.cash, "SIDEWAYS", "1", "USD"}, line{a.wallet, "CREDIT", "1", "USD"})
		requirePgError(t, err, codeCheckViolation, "postings_direction_valid")
	})

	t.Run("account fields", func(t *testing.T) {
		cases := []struct {
			name, code, currency, typ, normal, constraint string
		}{
			{"bad code", "-bad", "USD", "ASSET", "DEBIT", "accounts_code_format"},
			{"code with unicode look-alike", "caf\u00e9", "USD", "ASSET", "DEBIT", "accounts_code_format"},
			{"code too long", strings.Repeat("a", 65), "USD", "ASSET", "DEBIT", "accounts_code_format"},
			{"bad currency", "c1", "usd", "ASSET", "DEBIT", "accounts_currency_format"},
			{"bad type", "c2", "USD", "REVENUE", "DEBIT", "accounts_type_valid"},
			{"bad normal balance", "c3", "USD", "ASSET", "LEFT", "accounts_normal_balance_valid"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := db.App.Exec(ctx,
					`INSERT INTO accounts (code, currency, type, normal_balance) VALUES ($1,$2,$3,$4)`,
					tc.code, tc.currency, tc.typ, tc.normal)
				requirePgError(t, err, codeCheckViolation, tc.constraint)
			})
		}
	})

	t.Run("account code is unique", func(t *testing.T) {
		_, err := db.App.Exec(ctx,
			`INSERT INTO accounts (code, currency, type, normal_balance) VALUES ('bank-cash','USD','ASSET','DEBIT')`)
		requirePgError(t, err, codeUniqueViolation, "accounts_code_key")
	})

	t.Run("description length counts characters, not bytes", func(t *testing.T) {
		ok := strings.Repeat("é", 500)
		if _, err := db.App.Exec(ctx, `INSERT INTO transactions (description, created_at) VALUES ($1, now())`, ok); err == nil {
			// Inserted alone it has no postings, so the deferred trigger fires at
			// commit; with autocommit that is immediate. Either outcome shows the
			// length check itself did not reject 500 characters.
			t.Log("unexpected success without postings")
		} else {
			requirePgError(t, err, codeCheckViolation, "at least 2 postings")
		}
		tooLong := strings.Repeat("é", 501)
		_, err := db.App.Exec(ctx, `INSERT INTO transactions (description, created_at) VALUES ($1, now())`, tooLong)
		requirePgError(t, err, codeCheckViolation, "transactions_description_length")
	})
}

func TestApplicationRoleHasLeastPrivilege(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	denied := []struct{ name, sql string }{
		{"change account code", fmt.Sprintf(`UPDATE accounts SET code = 'renamed' WHERE id = '%s'`, a.cash)},
		{"change account currency", fmt.Sprintf(`UPDATE accounts SET currency = 'EUR' WHERE id = '%s'`, a.cash)},
		{"change account type", fmt.Sprintf(`UPDATE accounts SET type = 'EQUITY' WHERE id = '%s'`, a.cash)},
		{"change normal balance", fmt.Sprintf(`UPDATE accounts SET normal_balance = 'CREDIT' WHERE id = '%s'`, a.cash)},
		{"change allow_negative", fmt.Sprintf(`UPDATE accounts SET allow_negative = true WHERE id = '%s'`, a.cash)},
		{"delete an account", `DELETE FROM accounts`},
		{"drop a table", `DROP TABLE postings`},
		{"alter a table", `ALTER TABLE accounts ADD COLUMN x int`},
		{"create a table", `CREATE TABLE evil (x int)`},
		{"disable a trigger", `ALTER TABLE postings DISABLE TRIGGER ALL`},
		{"read migration history", `SELECT * FROM goose_db_version`},
	}
	for _, tc := range denied {
		t.Run("denied: "+tc.name, func(t *testing.T) {
			_, err := db.App.Exec(ctx, tc.sql)
			requirePgError(t, err, codeInsufficientPrivilege, "")
		})
	}

	allowed := []struct{ name, sql string }{
		{"update balance", fmt.Sprintf(`UPDATE accounts SET balance = 5 WHERE id = '%s'`, a.usd)},
		{"update last_posting_at", fmt.Sprintf(`UPDATE accounts SET last_posting_at = now() WHERE id = '%s'`, a.usd)},
		{"manage idempotency keys", `INSERT INTO idempotency_keys (key, request_hash, response_status, response_body) VALUES ('k', '\x00', 201, '{}')`},
		{"delete expired idempotency keys", `DELETE FROM idempotency_keys WHERE key = 'k'`},
	}
	for _, tc := range allowed {
		t.Run("allowed: "+tc.name, func(t *testing.T) {
			if _, err := db.App.Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExactNumericRoundTrip(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()
	a := seed(t, db.App)

	// 2^53+1 and 2^63 cannot survive a trip through float64 or int64.
	for _, amount := range []string{"9007199254740993", "9223372036854775808", "123456789012345678901234567890"} {
		id := mustPost(t, db.App, nil, pair(a.cash, a.wallet, amount, "USD")...)
		var got string
		if err := db.App.QueryRow(ctx,
			`SELECT amount::text FROM postings WHERE transaction_id = $1 AND direction = 'DEBIT'`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != amount {
			t.Errorf("stored %s, read back %s", amount, got)
		}
	}
}
