package service

import (
	"encoding/base64"
	"testing"

	"pgregory.net/rapid"
)

func TestCursor_RoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.Int64Min(1).Draw(t, "id")
		got, ok := decodeCursor(encodeCursor(id))
		if !ok || got != id {
			t.Fatalf("encode/decode of %d gave %d, %v", id, got, ok)
		}
	})
}

func TestCursor_RejectsGarbage(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, c := range map[string]string{
		"not base64":           "!!!not base64!!!",
		"empty payload":        enc(""),
		"wrong prefix":         enc("v2:5"),
		"no prefix":            enc("5"),
		"not a number":         enc("v1:abc"),
		"zero":                 enc("v1:0"),
		"negative":             enc("v1:-5"),
		"leading zeros":        enc("v1:007"),
		"plus sign":            enc("v1:+7"),
		"overflow":             enc("v1:9223372036854775808"),
		"with padding":         base64.URLEncoding.EncodeToString([]byte("v1:5")) + "=",
		"standard alphabet":    base64.StdEncoding.EncodeToString([]byte("v1:\xff\xfe")),
		"trailing whitespace":  enc("v1:5 "),
		"embedded second part": enc("v1:5:6"),
	} {
		t.Run(name, func(t *testing.T) {
			if id, ok := decodeCursor(c); ok {
				t.Errorf("cursor %q was accepted as %d", c, id)
			}
		})
	}
}
