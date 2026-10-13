package ledger

import (
	"math/big"
	"testing"

	"pgregory.net/rapid"
)

// edgeAmounts are the values most likely to expose precision and range bugs.
func edgeAmounts() []*big.Int {
	pow2 := func(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(1), n) }
	one := big.NewInt(1)
	return []*big.Int{
		big.NewInt(1),
		big.NewInt(2),
		big.NewInt(100),
		new(big.Int).Sub(pow2(53), one), // largest exactly-representable float64 integer
		pow2(53),
		new(big.Int).Add(pow2(53), one), // first integer float64 cannot represent
		new(big.Int).Sub(pow2(63), one), // int64 max
		pow2(63),                        // int64 max + 1
		pow2(64),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), // 1 ETH in wei
		MaxAmount(),
		new(big.Int).Sub(MaxAmount(), one),
	}
}

// genAmountBig draws a valid amount in [1, 10^30], mixing edge values with
// uniformly random ones.
func genAmountBig() *rapid.Generator[*big.Int] {
	edges := edgeAmounts()
	edgeGen := rapid.Custom(func(t *rapid.T) *big.Int {
		return new(big.Int).Set(rapid.SampledFrom(edges).Draw(t, "edge"))
	})
	randomGen := rapid.Custom(func(t *rapid.T) *big.Int {
		b := rapid.SliceOfN(rapid.Byte(), 1, 13).Draw(t, "bytes") // up to 104 bits > 10^30 (~100 bits)
		v := new(big.Int).SetBytes(b)
		v.Mod(v, MaxAmount())
		return v.Add(v, big.NewInt(1)) // [1, 10^30]
	})
	return rapid.OneOf(edgeGen, randomGen)
}

// genAmount draws a valid Amount.
func genAmount() *rapid.Generator[Amount] {
	return rapid.Custom(func(t *rapid.T) Amount {
		a, err := NewAmount(genAmountBig().Draw(t, "value"))
		if err != nil {
			t.Fatalf("generator produced an invalid amount: %v", err)
		}
		return a
	})
}

// mustAmount parses s or fails the test.
func mustAmount(tb testing.TB, s string) Amount {
	tb.Helper()
	a, err := ParseAmount(s)
	if err != nil {
		tb.Fatalf("ParseAmount(%q): %v", s, err)
	}
	return a
}
