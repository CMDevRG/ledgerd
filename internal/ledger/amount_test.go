package ledger

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestParseAmount_Valid(t *testing.T) {
	tests := []string{
		"1",
		"9",
		"10",
		"100",
		"9007199254740991",                // 2^53 - 1
		"9007199254740993",                // 2^53 + 1 (not representable as float64)
		"9223372036854775807",             // int64 max
		"9223372036854775808",             // int64 max + 1
		"1000000000000000000",             // 1 ETH in wei
		"1000000000000000000000000000000", // 10^30 (the maximum)
		"999999999999999999999999999999",  // 10^30 - 1
	}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			a, err := ParseAmount(in)
			if err != nil {
				t.Fatalf("ParseAmount(%q) error: %v", in, err)
			}
			if got := a.String(); got != in {
				t.Errorf("String() = %q, want %q", got, in)
			}
			if a.IsZero() {
				t.Error("parsed amount must not be the zero value")
			}
		})
	}
}

func TestParseAmount_Invalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"zero", "0"},
		{"leading zero", "01"},
		{"only zeros", "000"},
		{"negative", "-1"},
		{"explicit plus", "+1"},
		{"decimal point", "1.0"},
		{"decimal comma", "1,5"},
		{"exponent", "1e5"},
		{"hex", "0x10"},
		{"thousands separator", "1_000"},
		{"leading space", " 1"},
		{"trailing space", "1 "},
		{"newline", "1\n"},
		{"letters", "abc"},
		{"fullwidth digit", "１２３"},                            // Unicode digits must not be accepted
		{"arabic-indic digit", "١٢٣"},                         // ditto
		{"just above max", "1000000000000000000000000000001"}, // 10^30 + 1
		{"31 digits above max", "2000000000000000000000000000000"},
		{"too many digits", strings.Repeat("9", 32)},
		{"huge input", strings.Repeat("9", 1<<20)},
		{"nul byte", "1\x00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if a, err := ParseAmount(tc.in); err == nil {
				t.Errorf("ParseAmount(%q) = %s, want error", tc.in, a)
			}
		})
	}
}

func TestParseAmount_PrecisionAboveFloat64(t *testing.T) {
	// 2^53 + 1 cannot be represented by a float64. If the implementation ever
	// went through floating point it would come back as 9007199254740992.
	in := "9007199254740993"
	if got := mustAmount(t, in).String(); got != in {
		t.Errorf("precision lost: got %s, want %s", got, in)
	}
}

func TestNewAmount(t *testing.T) {
	if _, err := NewAmount(nil); err == nil {
		t.Error("nil must be rejected")
	}
	if _, err := NewAmount(big.NewInt(0)); err == nil {
		t.Error("zero must be rejected")
	}
	if _, err := NewAmount(big.NewInt(-5)); err == nil {
		t.Error("negative must be rejected")
	}
	over := new(big.Int).Add(MaxAmount(), big.NewInt(1))
	if _, err := NewAmount(over); err == nil {
		t.Error("10^30 + 1 must be rejected")
	}
	if _, err := NewAmount(MaxAmount()); err != nil {
		t.Errorf("10^30 must be accepted: %v", err)
	}
}

func TestAmount_IsImmutable(t *testing.T) {
	src := big.NewInt(42)
	a, err := NewAmount(src)
	if err != nil {
		t.Fatal(err)
	}
	src.SetInt64(7) // mutating the input must not change the amount
	if a.String() != "42" {
		t.Errorf("NewAmount did not copy its input: %s", a)
	}
	out := a.Big()
	out.SetInt64(7) // mutating the output must not change the amount
	if a.String() != "42" {
		t.Errorf("Big did not return a copy: %s", a)
	}
}

func TestAmount_ZeroValue(t *testing.T) {
	var a Amount
	if !a.IsZero() {
		t.Error("zero value must report IsZero")
	}
	if a.Big().Sign() != 0 {
		t.Error("zero value Big() must be 0")
	}
	if _, err := json.Marshal(a); err == nil {
		t.Error("marshaling the zero Amount must fail")
	}
}

func TestAmount_Cmp(t *testing.T) {
	small, big_ := mustAmount(t, "9"), mustAmount(t, "10")
	if small.Cmp(big_) != -1 || big_.Cmp(small) != 1 || small.Cmp(small) != 0 {
		t.Error("Cmp must compare numerically, not lexically")
	}
}

func TestAmount_JSON(t *testing.T) {
	t.Run("round trip as string", func(t *testing.T) {
		in := mustAmount(t, "9007199254740993")
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `"9007199254740993"` {
			t.Errorf("marshaled form = %s, want a JSON string", data)
		}
		var out Amount
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if out.Cmp(in) != 0 {
			t.Errorf("round trip changed value: %s != %s", out, in)
		}
	})

	t.Run("rejects non-string and invalid forms", func(t *testing.T) {
		for _, body := range []string{
			`10000`, // JSON number: could silently lose precision upstream
			`1.5`,   // JSON number with fraction
			`1e3`,   // JSON number with exponent
			`null`,  // null
			`true`,  // boolean
			`[]`,    // array
			`""`,    // empty string
			`"0"`,   // zero
			`"-5"`,  // negative
			`"007"`, // leading zeros
			`"1.0"`, // decimal
		} {
			var a Amount
			if err := json.Unmarshal([]byte(body), &a); err == nil {
				t.Errorf("Unmarshal(%s) succeeded with %s, want error", body, a)
			}
		}
	})

	t.Run("inside a struct", func(t *testing.T) {
		type dto struct {
			Amount Amount `json:"amount"`
		}
		var d dto
		if err := json.Unmarshal([]byte(`{"amount":"123"}`), &d); err != nil {
			t.Fatal(err)
		}
		if d.Amount.String() != "123" {
			t.Errorf("got %s", d.Amount)
		}
		if err := json.Unmarshal([]byte(`{"amount":123}`), &d); err == nil {
			t.Error("a JSON number in a struct field must be rejected")
		}
	})
}

// Property: formatting then parsing returns the same value, for every valid
// amount including the edge values around 2^53, 2^63 and 10^30.
func TestProperty_ParseStringRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := genAmountBig().Draw(t, "v")
		a, err := ParseAmount(v.String())
		if err != nil {
			t.Fatalf("valid amount %s rejected: %v", v, err)
		}
		if a.Big().Cmp(v) != 0 {
			t.Fatalf("round trip changed value: %s -> %s", v, a)
		}
	})
}

// Property: if arbitrary text is accepted, it must already be in canonical
// form (String() returns exactly the input). This guarantees one textual form
// per amount, which request hashing relies on.
func TestProperty_AcceptedInputIsCanonical(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.String(),
			rapid.StringMatching(`[0-9+\-.eE_ ]{0,34}`),
			rapid.StringMatching(`[1-9][0-9]{0,33}`),
		).Draw(t, "s")
		a, err := ParseAmount(s)
		if err != nil {
			return
		}
		if a.String() != s {
			t.Fatalf("accepted non-canonical input %q (canonical %q)", s, a.String())
		}
		if a.Big().Sign() <= 0 || a.Big().Cmp(MaxAmount()) > 0 {
			t.Fatalf("accepted out-of-range value %q", s)
		}
	})
}

// Property: the shared generator only yields valid amounts, and JSON
// round-trips them exactly. This also guards the generator itself, which the
// later domain property tests depend on.
func TestProperty_GeneratedAmountsRoundTripJSON(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genAmount().Draw(t, "a")
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var back Amount
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		if back.Cmp(a) != 0 {
			t.Fatalf("JSON round trip changed value: %s -> %s", a, back)
		}
	})
}
