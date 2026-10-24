package main

import "testing"

// The authenticated Connect carries the id_token as a length-prefixed
// protobuf string. When the next field's tag byte is a base64url character,
// the token must still end at its length, or the signature check fails.
func TestExtractFirstJWTStopsAtFieldLength(t *testing.T) {
	tok := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl"
	for _, next := range []byte{'B', '2', 'J', 'R', 'Z', 'b', 0x1a, 0x22} {
		payload := []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o', 0x12, byte(len(tok))}
		payload = append(payload, tok...)
		payload = append(payload, next, 0x03, 'x', 'y', 'z')
		if got := extractFirstJWT(payload); got != tok {
			t.Fatalf("next tag %q: got %q, want %q", next, got, tok)
		}
	}
	// A two-byte length (real tokens are ~1.3 KB).
	long := "eyJhbGciOiJSUzI1NiJ9." + repeatA(300) + ".c2ln"
	payload := append([]byte{0x12}, pbVarintRaw(uint64(len(long)))...)
	payload = append(append(payload, long...), 'B', 0x01, 'q')
	if got := extractFirstJWT(payload); got != long {
		t.Fatalf("two-byte length: got %d chars, want %d", len(got), len(long))
	}
}

func repeatA(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'A'
	}
	return string(b)
}

func pbVarintRaw(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}
