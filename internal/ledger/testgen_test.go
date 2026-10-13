package ledger

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/google/uuid"
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

// failer is the part of testing.TB that helpers need. It is satisfied by
// *testing.T, *testing.B and *rapid.T (which does not implement all of testing.TB).
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

// mustAmount parses s or fails the test.
func mustAmount(tb failer, s string) Amount {
	tb.Helper()
	a, err := ParseAmount(s)
	if err != nil {
		tb.Fatalf("ParseAmount(%q): %v", s, err)
	}
	return a
}

// ---- generators for transactions ----

// drawBigBetween draws a uniformly distributed integer in [lo, hi].
func drawBigBetween(t *rapid.T, label string, lo, hi *big.Int) *big.Int {
	span := new(big.Int).Sub(hi, lo)
	span.Add(span, big.NewInt(1))
	n := (span.BitLen()+7)/8 + 8 // extra bytes make the modulo bias negligible
	b := rapid.SliceOfN(rapid.Byte(), n, n).Draw(t, label)
	v := new(big.Int).SetBytes(b)
	v.Mod(v, span)
	return v.Add(v, lo)
}

// splitTotal splits total into between 1 and maxParts strictly positive parts
// that add up to exactly total.
func splitTotal(t *rapid.T, label string, total *big.Int, maxParts int) []*big.Int {
	want := rapid.IntRange(1, maxParts).Draw(t, label+"-parts")
	if total.IsInt64() && total.Int64() < int64(want) {
		want = int(total.Int64())
	}
	cuts := map[string]*big.Int{}
	for i := 0; i < want-1; i++ {
		c := drawBigBetween(t, fmt.Sprintf("%s-cut%d", label, i), big.NewInt(1), new(big.Int).Sub(total, big.NewInt(1)))
		cuts[c.String()] = c
	}
	points := []*big.Int{new(big.Int)}
	for _, c := range cuts {
		points = append(points, c)
	}
	sort.Slice(points[1:], func(i, j int) bool { return points[1+i].Cmp(points[1+j]) < 0 })
	points = append(points, new(big.Int).Set(total))

	parts := make([]*big.Int, 0, len(points)-1)
	for i := 1; i < len(points); i++ {
		parts = append(parts, new(big.Int).Sub(points[i], points[i-1]))
	}
	return parts
}

var testCurrencies = []Currency{"USD", "ETH", "USDT"}

// testAccountID returns a stable account id for (currency, index) so that
// generated requests reuse accounts, which is what exercises per-account logic.
func testAccountID(c Currency, i int) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s/%d", c, i)))
}

// genBalancedRequest draws a request that is valid and balanced in every
// currency it uses, with amounts that reach beyond 2^63 and up to 10^30.
func genBalancedRequest() *rapid.Generator[TransactionRequest] {
	return rapid.Custom(func(t *rapid.T) TransactionRequest {
		nCur := rapid.IntRange(1, 2).Draw(t, "currencies")
		chosen := rapid.SliceOfNDistinct(rapid.SampledFrom(testCurrencies), nCur, nCur, func(c Currency) Currency { return c }).Draw(t, "chosen")

		var postings []PostingInput
		for _, cur := range chosen {
			total := genAmountBig().Draw(t, "total-"+string(cur))
			for _, side := range []Side{Debit, Credit} {
				for _, part := range splitTotal(t, string(cur)+string(side), total, 4) {
					amt, err := NewAmount(part)
					if err != nil {
						t.Fatalf("invalid part %s: %v", part, err)
					}
					postings = append(postings, PostingInput{
						AccountID: testAccountID(cur, rapid.IntRange(0, 2).Draw(t, "acct")),
						Direction: side,
						Amount:    amt,
						Currency:  cur,
					})
				}
			}
		}
		order := rapid.Permutation(postings).Draw(t, "order")
		return TransactionRequest{Description: "generated", Postings: order}
	})
}
