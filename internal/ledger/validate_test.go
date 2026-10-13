package ledger

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"
)

// assertViolationOn fails unless err is a *ValidationError with a violation
// whose field starts with the given prefix.
func assertViolationOn(tb testing.TB, err error, fieldPrefix string) {
	tb.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		tb.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	for _, v := range ve.Violations {
		if strings.HasPrefix(v.Field, fieldPrefix) {
			return
		}
	}
	tb.Errorf("no violation on field %q in: %v", fieldPrefix, err)
}

var (
	acctA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	acctB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	acctC = uuid.MustParse("00000000-0000-0000-0000-00000000000c")
)

func posting(tb failer, acct uuid.UUID, side Side, amount string, cur Currency) PostingInput {
	tb.Helper()
	return PostingInput{AccountID: acct, Direction: side, Amount: mustAmount(tb, amount), Currency: cur}
}

func simpleRequest(tb failer) TransactionRequest {
	tb.Helper()
	return TransactionRequest{
		Description: "deposit",
		Postings: []PostingInput{
			posting(tb, acctA, Debit, "10000", "USD"),
			posting(tb, acctB, Credit, "10000", "USD"),
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Run("minimal two-posting request", func(t *testing.T) {
		if err := simpleRequest(t).Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("empty description is allowed", func(t *testing.T) {
		r := simpleRequest(t)
		r.Description = ""
		if err := r.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("one debit split across two credits", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "100", "USD"),
			posting(t, acctB, Credit, "60", "USD"),
			posting(t, acctC, Credit, "40", "USD"),
		}}
		if err := r.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("multi-currency, each currency balanced", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "100", "USD"),
			posting(t, acctB, Credit, "100", "USD"),
			posting(t, acctA, Debit, "5", "ETH"),
			posting(t, acctB, Credit, "5", "ETH"),
		}}
		if err := r.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("amounts beyond int64 and at the 10^30 limit", func(t *testing.T) {
		const max = "1000000000000000000000000000000"
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, max, "ETH"),
			posting(t, acctB, Credit, "9223372036854775808", "ETH"),
			posting(t, acctC, Credit, "999999999990776627963145224192", "ETH"),
		}}
		if err := r.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("description of 500 multi-byte characters", func(t *testing.T) {
		r := simpleRequest(t)
		r.Description = strings.Repeat("é", MaxDescriptionLength) // 1000 bytes, 500 characters
		if err := r.Validate(); err != nil {
			t.Fatalf("limit is in characters, not bytes: %v", err)
		}
	})
}

func TestValidate_PostingCount(t *testing.T) {
	build := func(pairs int) TransactionRequest {
		var ps []PostingInput
		for i := 0; i < pairs; i++ {
			ps = append(ps, posting(t, acctA, Debit, "1", "USD"), posting(t, acctB, Credit, "1", "USD"))
		}
		return TransactionRequest{Postings: ps}
	}
	if err := build(50).Validate(); err != nil { // exactly 100 postings
		t.Errorf("100 postings must be accepted: %v", err)
	}
	over := build(50)
	over.Postings = append(over.Postings, posting(t, acctC, Debit, "1", "USD")) // 101 postings
	assertViolationOn(t, over.Validate(), "postings")

	assertViolationOn(t, TransactionRequest{}.Validate(), "postings")
	one := TransactionRequest{Postings: []PostingInput{posting(t, acctA, Debit, "1", "USD")}}
	assertViolationOn(t, one.Validate(), "postings")
}

func TestValidate_StructuralViolations(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*TransactionRequest)
		wantField string
	}{
		{"description too long", func(r *TransactionRequest) { r.Description = strings.Repeat("x", MaxDescriptionLength+1) }, "description"},
		{"nil account id", func(r *TransactionRequest) { r.Postings[0].AccountID = uuid.Nil }, "postings[0].account_id"},
		{"bad direction", func(r *TransactionRequest) { r.Postings[1].Direction = "SIDEWAYS" }, "postings[1].direction"},
		{"empty direction", func(r *TransactionRequest) { r.Postings[0].Direction = "" }, "postings[0].direction"},
		{"zero amount", func(r *TransactionRequest) { r.Postings[0].Amount = Amount{} }, "postings[0].amount"},
		{"bad currency", func(r *TransactionRequest) { r.Postings[1].Currency = "usd" }, "postings[1].currency"},
		{"empty currency", func(r *TransactionRequest) { r.Postings[0].Currency = "" }, "postings[0].currency"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := simpleRequest(t)
			tc.mutate(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			assertViolationOn(t, err, tc.wantField)
			if errors.Is(err, ErrUnbalanced) {
				t.Error("a structural problem must not be reported as unbalanced")
			}
		})
	}
}

func TestValidate_ReportsAllViolationsAtOnce(t *testing.T) {
	r := TransactionRequest{
		Description: strings.Repeat("x", MaxDescriptionLength+1),
		Postings:    []PostingInput{{Direction: "?", Currency: "bad"}, {Direction: "?", Currency: "bad"}},
	}
	err := r.Validate()
	for _, f := range []string{"description", "postings[0].account_id", "postings[0].direction", "postings[0].amount", "postings[0].currency", "postings[1].account_id"} {
		assertViolationOn(t, err, f)
	}
}

func TestValidate_Unbalanced(t *testing.T) {
	t.Run("debits and credits differ", func(t *testing.T) {
		r := simpleRequest(t)
		r.Postings[1].Amount = mustAmount(t, "9999")
		err := r.Validate()
		if !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("want ErrUnbalanced, got %v", err)
		}
		var ub *UnbalancedError
		if !errors.As(err, &ub) || ub.Currency != "USD" || ub.Debits != "10000" || ub.Credits != "9999" {
			t.Errorf("unexpected details: %+v", ub)
		}
	})
	t.Run("off by one at a huge amount", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "1000000000000000000000000000000", "ETH"),
			posting(t, acctB, Credit, "999999999999999999999999999999", "ETH"),
		}}
		if err := r.Validate(); !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("want ErrUnbalanced, got %v", err)
		}
	})
	t.Run("only debits", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "5", "USD"),
			posting(t, acctB, Debit, "5", "USD"),
		}}
		if err := r.Validate(); !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("want ErrUnbalanced, got %v", err)
		}
	})
	t.Run("amounts in different currencies never offset each other", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "100", "USD"),
			posting(t, acctB, Credit, "100", "ETH"),
		}}
		err := r.Validate()
		var ub *UnbalancedError
		if !errors.As(err, &ub) {
			t.Fatalf("want *UnbalancedError, got %v", err)
		}
		if ub.Currency != "ETH" { // currencies are checked in sorted order
			t.Errorf("want the first unbalanced currency (ETH), got %s", ub.Currency)
		}
	})
	t.Run("one balanced currency does not hide an unbalanced one", func(t *testing.T) {
		r := TransactionRequest{Postings: []PostingInput{
			posting(t, acctA, Debit, "100", "USD"),
			posting(t, acctB, Credit, "100", "USD"),
			posting(t, acctA, Debit, "7", "ETH"),
			posting(t, acctB, Credit, "6", "ETH"),
		}}
		var ub *UnbalancedError
		if err := r.Validate(); !errors.As(err, &ub) || ub.Currency != "ETH" {
			t.Fatalf("want unbalanced ETH, got %v", err)
		}
	})
}

// Property: every generated balanced request validates, including amounts
// beyond 2^63, several currencies and several postings per account.
func TestProperty_BalancedRequestsAreValid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genBalancedRequest().Draw(t, "request")
		if err := r.Validate(); err != nil {
			t.Fatalf("balanced request rejected: %v", err)
		}
	})
}

// Property: flipping the direction of any single posting of a balanced request
// always makes it unbalanced (the imbalance is 2×amount, and amount > 0).
func TestProperty_FlippingOnePostingUnbalances(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genBalancedRequest().Draw(t, "request")
		i := rapid.IntRange(0, len(r.Postings)-1).Draw(t, "index")
		flipped := TransactionRequest{Postings: append([]PostingInput(nil), r.Postings...)}
		flipped.Postings[i].Direction = flipped.Postings[i].Direction.Opposite()
		if err := flipped.Validate(); !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("flipping posting %d should unbalance, got %v", i, err)
		}
	})
}

// Property (oracle): for arbitrary, mostly invalid input, Validate never
// panics, and its verdict agrees with an independent computation of the
// per-currency imbalance.
func TestProperty_ValidateAgreesWithOracle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 6).Draw(t, "n")
		var r TransactionRequest
		imbalance := map[Currency]*big.Int{}
		structurallyValid := n >= MinPostings
		for i := 0; i < n; i++ {
			p := PostingInput{
				AccountID: rapid.SampledFrom([]uuid.UUID{uuid.Nil, acctA, acctB}).Draw(t, "acct"),
				Direction: rapid.SampledFrom([]Side{Debit, Credit, "", "X"}).Draw(t, "dir"),
				Currency:  rapid.SampledFrom([]Currency{"USD", "ETH", "", "usd"}).Draw(t, "cur"),
			}
			if rapid.Bool().Draw(t, "hasAmount") {
				p.Amount = mustAmount(t, rapid.SampledFrom([]string{"1", "2", "3", "10"}).Draw(t, "amt"))
			}
			r.Postings = append(r.Postings, p)

			_, curErr := ParseCurrency(string(p.Currency))
			if p.AccountID == uuid.Nil || !p.Direction.Valid() || p.Amount.IsZero() || curErr != nil {
				structurallyValid = false
				continue
			}
			if imbalance[p.Currency] == nil {
				imbalance[p.Currency] = new(big.Int)
			}
			if p.Direction == Debit {
				imbalance[p.Currency].Add(imbalance[p.Currency], p.Amount.Big())
			} else {
				imbalance[p.Currency].Sub(imbalance[p.Currency], p.Amount.Big())
			}
		}

		err := r.Validate()
		switch {
		case !structurallyValid:
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
		default:
			balanced := true
			for _, v := range imbalance {
				if v.Sign() != 0 {
					balanced = false
				}
			}
			if balanced && err != nil {
				t.Fatalf("balanced input rejected: %v", err)
			}
			if !balanced && !errors.Is(err, ErrUnbalanced) {
				t.Fatalf("unbalanced input accepted or misclassified: %v", err)
			}
		}
		_ = fmt.Sprint(err) // exercise Error() for every outcome
	})
}
