package main

import (
	"bytes"
	"testing"
)

// The bytes the live client actually sent: 0a07 "Hansolo" 10 04 18 00.
func TestCreateCharacterRoundTrip(t *testing.T) {
	req := append(pbBytes(1, []byte("Hansolo")), pbVarint(2, 4)...)
	req = append(req, pbVarint(3, 0)...)

	got := parseCreateCharacter(req)
	if got.Name != "Hansolo" || got.Class != 4 || got.Flags != 0 {
		t.Fatalf("parse: %+v", got)
	}

	charMu.Lock()
	charList = nil
	charNext = 1
	charMu.Unlock()

	if body := readCharacterListBody(); !bytes.Equal(body, pbVarint(1, 0)) {
		t.Fatalf("empty roster should be field 1 only, got %x", body)
	}

	got.ID = 7
	charMu.Lock()
	charList = append(charList, got)
	charMu.Unlock()

	body := readCharacterListBody()
	want := append(pbVarint(1, 0), pbBytes(2, characterSummary(got))...)
	if !bytes.Equal(body, want) {
		t.Fatalf("roster body\n got %x\nwant %x", body, want)
	}
	if !bytes.Contains(body, []byte("Hansolo")) {
		t.Fatalf("name missing from roster body %x", body)
	}
}
