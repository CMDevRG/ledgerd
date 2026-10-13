package ledger

import (
	"strings"
	"testing"
)

func TestSide(t *testing.T) {
	if Debit.Opposite() != Credit || Credit.Opposite() != Debit {
		t.Error("Opposite must swap DEBIT and CREDIT")
	}
	for _, s := range []Side{Debit, Credit} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []Side{"", "debit", "Credit", "BOTH", "DEBIT "} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestAccountType_Valid(t *testing.T) {
	for _, ty := range []AccountType{Asset, Liability, Equity, Income, Expense} {
		if !ty.Valid() {
			t.Errorf("%q should be valid", ty)
		}
	}
	for _, ty := range []AccountType{"", "asset", "REVENUE", "ASSET "} {
		if ty.Valid() {
			t.Errorf("%q should be invalid", ty)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	valid := []string{"USD", "EUR", "ETH", "BTC", "USDT", "USDC", "US", "ABCDE12345"}
	for _, in := range valid {
		if c, err := ParseCurrency(in); err != nil || string(c) != in {
			t.Errorf("ParseCurrency(%q) = %q, %v; want ok", in, c, err)
		}
	}
	invalid := []string{"", "A", "usd", "Usd", "USD ", " USD", "USD\n", "US-D", "US_D", "ÜSD", "ABCDEFGHIJK", "U$D"}
	for _, in := range invalid {
		if _, err := ParseCurrency(in); err == nil {
			t.Errorf("ParseCurrency(%q) should fail", in)
		}
	}
}

func validNewAccount() NewAccount {
	return NewAccount{Code: "bank-cash", Currency: "USD", Type: Asset, NormalBalance: Debit}
}

func TestNewAccount_Validate_Valid(t *testing.T) {
	for _, code := range []string{"a", "bank-cash", "user:123.wallet_1", "A9", strings.Repeat("a", 64)} {
		n := validNewAccount()
		n.Code = code
		if err := n.Validate(); err != nil {
			t.Errorf("code %q should be valid: %v", code, err)
		}
	}
}

func TestNewAccount_Validate_Invalid(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*NewAccount)
		wantField string
	}{
		{"empty code", func(n *NewAccount) { n.Code = "" }, "code"},
		{"leading space", func(n *NewAccount) { n.Code = " x" }, "code"},
		{"leading dash", func(n *NewAccount) { n.Code = "-x" }, "code"},
		{"leading dot", func(n *NewAccount) { n.Code = ".x" }, "code"},
		{"space inside", func(n *NewAccount) { n.Code = "a b" }, "code"},
		{"slash", func(n *NewAccount) { n.Code = "a/b" }, "code"},
		{"non-ascii", func(n *NewAccount) { n.Code = "ñandú" }, "code"},
		{"too long", func(n *NewAccount) { n.Code = strings.Repeat("a", 65) }, "code"},
		{"trailing newline", func(n *NewAccount) { n.Code = "abc\n" }, "code"},
		{"bad currency", func(n *NewAccount) { n.Currency = "usd" }, "currency"},
		{"bad type", func(n *NewAccount) { n.Type = "REVENUE" }, "type"},
		{"empty type", func(n *NewAccount) { n.Type = "" }, "type"},
		{"bad normal balance", func(n *NewAccount) { n.NormalBalance = "LEFT" }, "normal_balance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := validNewAccount()
			tc.mutate(&n)
			err := n.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			assertViolationOn(t, err, tc.wantField)
		})
	}
}

func TestNewAccount_Validate_ReportsAllViolations(t *testing.T) {
	err := NewAccount{Code: "", Currency: "x", Type: "?", NormalBalance: "?"}.Validate()
	for _, f := range []string{"code", "currency", "type", "normal_balance"} {
		assertViolationOn(t, err, f)
	}
}
