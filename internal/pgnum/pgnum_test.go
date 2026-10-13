package pgnum

import (
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func bi(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad integer " + s)
	}
	return v
}

func TestScanNumeric_NormalisesMantissaAndExponent(t *testing.T) {
	tests := []struct {
		name string
		in   pgtype.Numeric
		want string
	}{
		{"zero", pgtype.Numeric{Int: big.NewInt(0), Valid: true}, "0"},
		{"plain", pgtype.Numeric{Int: big.NewInt(12345), Valid: true}, "12345"},
		{"negative", pgtype.Numeric{Int: big.NewInt(-7), Valid: true}, "-7"},
		{"positive exponent (binary format)", pgtype.Numeric{Int: big.NewInt(1), Exp: 21, Valid: true}, "1000000000000000000000"},
		{"positive exponent with digits", pgtype.Numeric{Int: big.NewInt(123), Exp: 4, Valid: true}, "1230000"},
		{"trailing zeros after the point", pgtype.Numeric{Int: big.NewInt(1500), Exp: -2, Valid: true}, "15"},
		{"beyond int64", pgtype.Numeric{Int: bi("9223372036854775808"), Valid: true}, "9223372036854775808"},
		{"10^30", pgtype.Numeric{Int: big.NewInt(1), Exp: 30, Valid: true}, "1000000000000000000000000000000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got Int
			if err := got.ScanNumeric(tc.in); err != nil {
				t.Fatal(err)
			}
			if got.V.String() != tc.want {
				t.Errorf("got %s, want %s", got.V, tc.want)
			}
		})
	}
}

func TestScanNumeric_Rejects(t *testing.T) {
	tests := []struct {
		name string
		in   pgtype.Numeric
	}{
		{"NULL", pgtype.Numeric{}},
		{"NaN", pgtype.Numeric{NaN: true, Valid: true}},
		{"+Infinity", pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}},
		{"-Infinity", pgtype.Numeric{InfinityModifier: pgtype.NegativeInfinity, Valid: true}},
		{"fraction", pgtype.Numeric{Int: big.NewInt(15), Exp: -1, Valid: true}},
		{"no mantissa", pgtype.Numeric{Valid: true}},
		{"absurd exponent", pgtype.Numeric{Int: big.NewInt(1), Exp: 100000, Valid: true}},
		{"absurd negative exponent", pgtype.Numeric{Int: big.NewInt(1), Exp: -100000, Valid: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got Int
			if err := got.ScanNumeric(tc.in); err == nil {
				t.Errorf("expected an error, got %s", got.V)
			}
		})
	}
}

func TestNumericValue(t *testing.T) {
	in := bi("123456789012345678901234567890")
	n, err := FromBig(in).NumericValue()
	if err != nil {
		t.Fatal(err)
	}
	if !n.Valid || n.Exp != 0 || n.Int.Cmp(in) != 0 {
		t.Errorf("unexpected encoding: %+v", n)
	}
	if _, err := (Int{}).NumericValue(); err == nil {
		t.Error("encoding a nil integer must fail instead of writing 0")
	}
}

func TestFromBig_Copies(t *testing.T) {
	src := big.NewInt(5)
	i := FromBig(src)
	src.SetInt64(9)
	if i.V.Int64() != 5 {
		t.Error("FromBig must copy its argument")
	}
	out := i.Big()
	out.SetInt64(9)
	if i.V.Int64() != 5 {
		t.Error("Big must return a copy")
	}
	if FromBig(nil).Big().Sign() != 0 {
		t.Error("nil converts to the zero Int")
	}
}
