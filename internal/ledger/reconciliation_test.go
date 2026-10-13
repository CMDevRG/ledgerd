package ledger

import (
	"math/big"
	"testing"
)

func totals(debits, credits int64) CurrencyTotals {
	return CurrencyTotals{Currency: "USD", Debits: big.NewInt(debits), Credits: big.NewInt(credits)}
}

func TestCurrencyTotals_Balanced(t *testing.T) {
	if !totals(5, 5).Balanced() || !totals(0, 0).Balanced() {
		t.Error("equal totals are balanced")
	}
	if totals(5, 4).Balanced() || totals(4, 5).Balanced() {
		t.Error("different totals are not balanced")
	}
	huge := new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
	if (CurrencyTotals{Debits: huge, Credits: new(big.Int).Add(huge, big.NewInt(1))}).Balanced() {
		t.Error("a difference of one unit at 10^30 must be detected")
	}
}

func TestReconciliationReport_OK(t *testing.T) {
	clean := ReconciliationReport{Currencies: []CurrencyTotals{totals(3, 3), totals(0, 0)}}
	if !clean.OK() {
		t.Error("a consistent report is OK")
	}
	if !(ReconciliationReport{}).OK() {
		t.Error("an empty ledger is consistent")
	}

	bad := map[string]ReconciliationReport{
		"mismatch":     {Mismatches: []BalanceMismatch{{}}},
		"chain break":  {Deep: true, ChainBreaks: []ChainBreak{{}}},
		"unbalanced":   {Currencies: []CurrencyTotals{totals(3, 3), totals(2, 1)}},
		"all problems": {Mismatches: []BalanceMismatch{{}}, ChainBreaks: []ChainBreak{{}}, Currencies: []CurrencyTotals{totals(1, 2)}},
	}
	for name, r := range bad {
		if r.OK() {
			t.Errorf("%s: report must not be OK", name)
		}
	}
}
