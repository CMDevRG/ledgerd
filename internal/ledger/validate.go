package ledger

import (
	"fmt"
	"math/big"
	"sort"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Validate checks the structure of the request and then that, for every
// currency, total debits equal total credits.
//
// It returns a *ValidationError listing every structural problem found. Only
// when the structure is valid does it check the balance and, if it fails,
// return an *UnbalancedError (errors.Is(err, ErrUnbalanced)). Balance sums are
// meaningless when postings are malformed, so the two checks are not mixed.
func (r TransactionRequest) Validate() error {
	var vs violations

	if utf8.RuneCountInString(r.Description) > MaxDescriptionLength {
		vs.add("description", "must be at most %d characters", MaxDescriptionLength)
	}

	n := len(r.Postings)
	switch {
	case n < MinPostings:
		vs.add("postings", "must contain at least %d postings", MinPostings)
	case n > MaxPostings:
		vs.add("postings", "must contain at most %d postings", MaxPostings)
	}

	for i, p := range r.Postings {
		field := func(name string) string { return fmt.Sprintf("postings[%d].%s", i, name) }
		if p.AccountID == uuid.Nil {
			vs.add(field("account_id"), "is required")
		}
		if !p.Direction.Valid() {
			vs.add(field("direction"), "must be DEBIT or CREDIT")
		}
		if p.Amount.IsZero() {
			vs.add(field("amount"), "is required and must be between 1 and 10^30")
		}
		if _, err := ParseCurrency(string(p.Currency)); err != nil {
			vs.add(field("currency"), "%s", err.Error())
		}
	}
	if err := vs.err(); err != nil {
		return err
	}
	return r.checkBalanced()
}

// checkBalanced verifies Σdebits = Σcredits for every currency present.
// Currencies are checked in sorted order so the reported error is deterministic.
func (r TransactionRequest) checkBalanced() error {
	type totals struct{ debits, credits *big.Int }
	byCurrency := map[Currency]*totals{}
	for _, p := range r.Postings {
		t, ok := byCurrency[p.Currency]
		if !ok {
			t = &totals{debits: new(big.Int), credits: new(big.Int)}
			byCurrency[p.Currency] = t
		}
		if p.Direction == Debit {
			t.debits.Add(t.debits, p.Amount.Big())
		} else {
			t.credits.Add(t.credits, p.Amount.Big())
		}
	}

	currencies := make([]Currency, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Slice(currencies, func(i, j int) bool { return currencies[i] < currencies[j] })

	for _, c := range currencies {
		t := byCurrency[c]
		if t.debits.Cmp(t.credits) != 0 {
			return &UnbalancedError{Currency: c, Debits: t.debits.String(), Credits: t.credits.String()}
		}
	}
	return nil
}
