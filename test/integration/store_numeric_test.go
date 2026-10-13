package integration

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pgregory.net/rapid"

	"github.com/google/uuid"
	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/pgnum"
	"github.com/radadev/ledgerd/internal/store"
	"github.com/radadev/ledgerd/internal/store/db"
)

var maxNumeric38 = new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil), big.NewInt(1)) // 10^38 - 1

// genNumeric draws integers across the whole NUMERIC(38,0) range, mixing
// boundary values (powers of ten, which are decoded with a positive exponent
// in the binary wire format; values around 2^53 and 2^63) with random ones.
func genNumeric() *rapid.Generator[*big.Int] {
	pow10 := func(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }
	pow2 := func(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(1), n) }
	edges := []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(9), big.NewInt(10), big.NewInt(9999), big.NewInt(10000), big.NewInt(10001),
		pow10(8), pow10(21), pow10(30), pow10(37),
		new(big.Int).Sub(pow2(63), big.NewInt(1)), pow2(63), new(big.Int).Add(pow2(53), big.NewInt(1)),
		new(big.Int).Set(maxNumeric38),
	}
	return rapid.Custom(func(t *rapid.T) *big.Int {
		var v *big.Int
		if rapid.Bool().Draw(t, "edge") {
			v = new(big.Int).Set(rapid.SampledFrom(edges).Draw(t, "value"))
		} else {
			b := rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "bytes")
			v = new(big.Int).SetBytes(b)
			v.Mod(v, new(big.Int).Add(maxNumeric38, big.NewInt(1)))
		}
		if rapid.Bool().Draw(t, "negative") {
			v.Neg(v)
		}
		return v
	})
}

// A NUMERIC may arrive as {Int: 1, Exp: 21} or {Int: 10^21, Exp: 0}, depending
// on the wire format. Both formats must give exactly the same integer.
func TestNumeric_RoundTripInBothWireFormats(t *testing.T) {
	db := server.NewDB(t)
	ctx := context.Background()

	for name, format := range map[string]int16{"text": pgx.TextFormatCode, "binary": pgx.BinaryFormatCode} {
		t.Run(name, func(t *testing.T) {
			rapid.Check(t, func(rt *rapid.T) {
				in := genNumeric().Draw(rt, "n")

				var out pgnum.Int
				err := db.App.QueryRow(ctx, `SELECT $1::numeric(38,0)`,
					pgx.QueryResultFormats{format}, pgnum.FromBig(in)).Scan(&out)
				if err != nil {
					rt.Fatalf("round trip of %s failed: %v", in, err)
				}
				if out.V.Cmp(in) != 0 {
					rt.Fatalf("sent %s, got back %s", in, out.V)
				}
			})
		})
	}
}

// The same guarantee through a real column and a numeric[] parameter, using
// the sqlc-generated code the service relies on.
func TestNumeric_RoundTripThroughColumnsAndArrays(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})

	const n = 4
	ids := make([]uuid.UUID, n)
	for i := range ids {
		a, err := st.CreateAccount(ctx, ledger.NewAccount{
			Code: "acct-" + string(rune('a'+i)), Currency: "ETH", Type: ledger.Asset,
			NormalBalance: ledger.Debit, AllowNegative: true, // negative values are legal here
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = a.ID
	}

	rapid.Check(t, func(rt *rapid.T) {
		want := make([]pgnum.Int, n)
		for i := range want {
			want[i] = pgnum.FromBig(genNumeric().Draw(rt, "balance"))
		}
		if err := st.Queries().UpdateAccountBalances(ctx, db.UpdateAccountBalancesParams{
			RecordedAt: time.Now(), Ids: ids, Balances: want,
		}); err != nil {
			rt.Fatalf("update: %v", err)
		}
		for i, id := range ids {
			got, err := st.GetAccount(ctx, id)
			if err != nil {
				rt.Fatal(err)
			}
			if got.Balance.Cmp(want[i].V) != 0 {
				rt.Fatalf("account %d: wrote %s, read %s", i, want[i].V, got.Balance)
			}
		}
	})
}

func TestNumeric_DatabaseRejectsMoreThan38Digits(t *testing.T) {
	database := server.NewDB(t)
	ctx := context.Background()
	st := store.New(database.App, store.Config{})
	a, err := st.CreateAccount(ctx, ledger.NewAccount{Code: "big", Currency: "ETH", Type: ledger.Asset, NormalBalance: ledger.Debit, AllowNegative: true})
	if err != nil {
		t.Fatal(err)
	}

	tooBig := new(big.Int).Add(maxNumeric38, big.NewInt(1)) // 10^38
	err = st.Queries().UpdateAccountBalances(ctx, db.UpdateAccountBalancesParams{
		RecordedAt: time.Now(), Ids: []uuid.UUID{a.ID}, Balances: []pgnum.Int{pgnum.FromBig(tooBig)},
	})
	requirePgError(t, err, "22003", "numeric field overflow")
}
