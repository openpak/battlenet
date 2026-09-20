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
	got.Level = 1
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

// A deploy must not cost the player their character.
func TestRosterSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATTLENET_CHARACTER_STORE", dir+"/characters.json")

	charMu.Lock()
	charList = nil
	charNext = 1
	charMu.Unlock()

	if _, ok := handleCharacterRPC(&gatewaySession{remote: "test"}, d2rHeader{
		service: characterHash,
		method:  charCreate,
		body:    append(append(pbBytes(1, []byte("Hansolo")), pbVarint(2, 4)...), pbVarint(3, 0)...),
	}); !ok {
		t.Fatal("CreateCharacter not handled")
	}

	// Wipe memory the way a restart does, then reload from disk.
	charMu.Lock()
	charList = nil
	charNext = 1
	charMu.Unlock()
	loadCharacters()

	charMu.Lock()
	defer charMu.Unlock()
	if len(charList) != 1 || charList[0].Name != "Hansolo" || charList[0].Class != 4 || charList[0].Level != 1 {
		t.Fatalf("roster after restart: %+v", charList)
	}
	if charNext != 2 {
		t.Fatalf("next id after restart = %d, want 2", charNext)
	}
}
