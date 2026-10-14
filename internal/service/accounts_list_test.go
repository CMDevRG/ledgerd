package service

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/CMDevRG/ledgerd/internal/store"
)

func TestAccountCursor_RoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		micros := rapid.Int64Range(1, 1<<60).Draw(t, "micros")
		var raw [16]byte
		copy(raw[:], rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "id"))
		in := store.AccountCursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: uuid.UUID(raw)}

		out, ok := decodeAccountCursor(encodeAccountCursor(in))
		if !ok || !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
			t.Fatalf("round trip of %+v gave %+v, %v", in, out, ok)
		}
	})
}

func TestAccountCursor_RejectsGarbage(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	const id = "3f2b8a52-2f8e-4c61-9a4e-0d6f4f0e7c11"
	for name, c := range map[string]string{
		"not base64":        "!!! not base64 !!!",
		"empty":             enc(""),
		"wrong prefix":      enc("v2:1700000000000000:" + id),
		"no separator":      enc("v1:1700000000000000"),
		"zero time":         enc("v1:0:" + id),
		"negative time":     enc("v1:-5:" + id),
		"leading zeros":     enc("v1:01700000000000000:" + id),
		"time not numeric":  enc("v1:soon:" + id),
		"time overflow":     enc("v1:99999999999999999999:" + id),
		"id not a uuid":     enc("v1:1700000000000000:not-a-uuid"),
		"id without dashes": enc("v1:1700000000000000:3f2b8a522f8e4c619a4e0d6f4f0e7c11"),
		"id in braces":      enc("v1:1700000000000000:{" + id + "}"),
		"id in upper case":  enc("v1:1700000000000000:3F2B8A52-2F8E-4C61-9A4E-0D6F4F0E7C11"),
		"extra field":       enc("v1:1700000000000000:" + id + ":x"),
		"a postings cursor": enc("v1:42"),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := decodeAccountCursor(c); ok {
				t.Errorf("cursor %q was accepted as %+v", c, got)
			}
		})
	}
}
