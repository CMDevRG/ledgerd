package integration

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/store"
	"github.com/radadev/ledgerd/internal/store/db"
)

func TestCreateAndGetAccount(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})

	in := ledger.NewAccount{Code: "user:42.wallet", Currency: "USDT", Type: ledger.Liability, NormalBalance: ledger.Credit, AllowNegative: false}
	created, err := st.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	if created.ID == uuid.Nil || created.CreatedAt.IsZero() {
		t.Errorf("id and creation time must be generated: %+v", created)
	}
	if created.Code != in.Code || created.Currency != in.Currency || created.Type != in.Type ||
		created.NormalBalance != in.NormalBalance || created.AllowNegative != in.AllowNegative {
		t.Errorf("fields changed on the way in: %+v", created)
	}
	if created.Balance == nil || created.Balance.Sign() != 0 {
		t.Errorf("new account must start at zero, got %v", created.Balance)
	}
	if created.LastPostingAt != nil {
		t.Errorf("new account has no postings yet, LastPostingAt = %v", created.LastPostingAt)
	}

	got, err := st.GetAccount(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Code != created.Code || got.Balance.Cmp(created.Balance) != 0 || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("Get returned %+v, want %+v", got, created)
	}
}

func TestCreateAccount_DuplicateCode(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})

	first := ledger.NewAccount{Code: "dup", Currency: "USD", Type: ledger.Asset, NormalBalance: ledger.Debit}
	if _, err := st.CreateAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Same code, different everything else: the code alone must conflict.
	_, err := st.CreateAccount(ctx, ledger.NewAccount{Code: "dup", Currency: "EUR", Type: ledger.Equity, NormalBalance: ledger.Credit})
	if !errors.Is(err, ledger.ErrAccountCodeExists) {
		t.Fatalf("want ErrAccountCodeExists, got %v", err)
	}

	var n int
	if err := database.Super.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE code = 'dup'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d accounts with the code, want 1", n)
	}
}

func TestCreateAccount_DatabaseConstraintsBackUpValidation(t *testing.T) {
	database := server.NewDB(t)
	st := store.New(database.App, store.Config{})

	// Deliberately skip ledger validation: the database must still refuse it,
	// and the error must not be mistaken for a duplicate code.
	_, err := st.CreateAccount(context.Background(), ledger.NewAccount{Code: "ok", Currency: "usd", Type: ledger.Asset, NormalBalance: ledger.Debit})
	if err == nil {
		t.Fatal("an invalid currency must be rejected by the database")
	}
	if errors.Is(err, ledger.ErrAccountCodeExists) {
		t.Error("a check violation must not be reported as a duplicate code")
	}
	requirePgError(t, err, codeCheckViolation, "accounts_currency_format")
}

func TestGetAccount_NotFound(t *testing.T) {
	database := server.NewDB(t)
	st := store.New(database.App, store.Config{})

	missing := uuid.New()
	_, err := st.GetAccount(context.Background(), missing)
	var nf *ledger.AccountNotFoundError
	if !errors.Is(err, ledger.ErrNotFound) || !errors.As(err, &nf) || nf.ID != missing {
		t.Fatalf("want AccountNotFoundError for %s, got %v", missing, err)
	}
}

func TestLockAccounts(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})

	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = newAccount(t, st, "lock-"+string(rune('a'+i)))
	}

	t.Run("returns every requested account, deduplicated, whatever the input order", func(t *testing.T) {
		request := []uuid.UUID{ids[3], ids[0], ids[3], ids[4], ids[1], ids[0]}
		err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
			got, err := store.LockAccounts(ctx, q, request)
			if err != nil {
				return err
			}
			if len(got) != 4 {
				t.Errorf("got %d accounts, want 4 distinct", len(got))
			}
			for _, id := range []uuid.UUID{ids[0], ids[1], ids[3], ids[4]} {
				if got[id].ID != id {
					t.Errorf("account %s missing from the result", id)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the database hands rows back in ascending id order", func(t *testing.T) {
		shuffled := []uuid.UUID{ids[2], ids[4], ids[0], ids[3], ids[1]}
		err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
			rows, err := q.LockAccounts(ctx, shuffled)
			if err != nil {
				return err
			}
			for i := 1; i < len(rows); i++ {
				if bytes.Compare(rows[i-1].ID[:], rows[i].ID[:]) >= 0 {
					t.Errorf("rows %d and %d are out of order: %s then %s", i-1, i, rows[i-1].ID, rows[i].ID)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reports the lowest missing id", func(t *testing.T) {
		low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		high := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
		err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
			_, err := store.LockAccounts(ctx, q, []uuid.UUID{high, ids[0], low})
			return err
		})
		var nf *ledger.AccountNotFoundError
		if !errors.As(err, &nf) || nf.ID != low {
			t.Fatalf("want AccountNotFoundError for %s, got %v", low, err)
		}
	})

	t.Run("no ids is not an error", func(t *testing.T) {
		err := st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
			got, err := store.LockAccounts(ctx, q, nil)
			if len(got) != 0 {
				t.Errorf("got %d accounts, want 0", len(got))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("locks are held until the transaction ends", func(t *testing.T) {
		held := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- st.InTx(ctx, func(ctx context.Context, q *db.Queries) error {
				if _, err := store.LockAccounts(ctx, q, ids[:1]); err != nil {
					return err
				}
				close(held)
				<-release
				return nil
			})
		}()
		<-held

		// A second session must not be able to lock the same row meanwhile.
		probe, err := database.Owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Rollback(ctx) //nolint:errcheck // probe is read-only
		var locked uuid.UUID
		err = probe.QueryRow(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE NOWAIT`, ids[0]).Scan(&locked)
		requirePgError(t, err, "55P03", "")

		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
