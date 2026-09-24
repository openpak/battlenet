package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// decodeTestFields splits a protobuf body into varint and bytes fields.
func decodeTestFields(t *testing.T, body []byte) (varints map[uint64]uint64, datas map[uint64][]byte) {
	t.Helper()
	varints = make(map[uint64]uint64)
	datas = make(map[uint64][]byte)
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			t.Fatalf("bad tag in %x", body)
		}
		body = body[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				t.Fatalf("bad varint field %d", num)
			}
			varints[uint64(num)] = v
			body = body[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				t.Fatalf("bad bytes field %d", num)
			}
			cp := append([]byte(nil), v...)
			datas[uint64(num)] = cp
			body = body[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, body)
			if m < 0 {
				t.Fatalf("bad field %d type %v", num, typ)
			}
			body = body[m:]
		}
	}
	return varints, datas
}

// The bytes the live client actually sent: 0a07 "Hansolo" 10 04 18 00.
func TestCreateCharacterRoundTrip(t *testing.T) {
	req := append(pbBytes(1, []byte("Hansolo")), pbVarint(2, 4)...)
	req = append(req, pbVarint(3, 0)...)

	got := parseCreateCharacter(req)
	if got.Name != "Hansolo" || got.Class != 4 || got.Flags != 0 || got.Field4 != 0 {
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

func TestCreateResponseCarriesIDOnly(t *testing.T) {
	c := d2rCharacter{Name: "Hansolo", Class: 4, Flags: 0, ID: 42, Level: 1}
	body := createCharacterBody(c)
	varints, datas := decodeTestFields(t, body)
	if len(datas) != 0 {
		t.Fatalf("create response should have no length-delimited fields, got %v", datas)
	}
	id, ok := varints[1]
	if !ok || id != 42 {
		t.Fatalf("create response field 1 = %v (present=%v), want 42", id, ok)
	}
	if _, ok := varints[2]; ok {
		t.Fatalf("create response must not contain field 2, got %x", body)
	}
	if len(varints) != 1 {
		t.Fatalf("create response should contain only field 1, got %v", varints)
	}
}

func TestCharacterSummaryEncoding(t *testing.T) {
	c := d2rCharacter{Name: "Hansolo", Class: 4, Flags: 0, ID: 7, Level: 1}
	body := characterSummary(c)
	varints, datas := decodeTestFields(t, body)

	// Field 1 is the character ID, not the level.
	id, ok := varints[1]
	if !ok || id != 7 {
		t.Fatalf("summary field 1 = %v (present=%v), want ID 7", id, ok)
	}

	// Field 2 is the name bytes.
	if string(datas[2]) != "Hansolo" {
		t.Fatalf("summary field 2 = %q, want %q", datas[2], "Hansolo")
	}

	// Field 3 is the populated fixed-offset statstring used by the working
	// online character-select path.
	raw, ok := datas[3]
	if !ok {
		t.Fatalf("summary field 3 missing in %x", body)
	}
	if len(raw) != 31 || raw[13] != 5 || raw[25] != 1 {
		t.Fatalf("summary statstring length/class/level = %d/%d/%d, want 31/5/1", len(raw), raw[13], raw[25])
	}
}

// The bytes the live client sent for an expansion create:
// 0a07 "Hansolo" 10 04 18 00 20 02 — field 4 = 2.
func TestExpansionCreatePreservesField4(t *testing.T) {
	req := append(append(append(pbBytes(1, []byte("Hansolo")), pbVarint(2, 4)...), pbVarint(3, 0)...), pbVarint(4, 2)...)
	got := parseCreateCharacter(req)
	if got.Name != "Hansolo" || got.Class != 4 || got.Flags != 0 || got.Field4 != 2 {
		t.Fatalf("expansion parse: %+v", got)
	}

	_, datas := decodeTestFields(t, characterSummary(d2rCharacter{
		Name: "Hansolo", Class: 4, Flags: 0, Field4: 2, ID: 2, Level: 1,
	}))
	raw := datas[3]
	flags := uint64(raw[26]&0x7f) | uint64(raw[27]&0x7f)<<7
	if flags != 0x20 {
		t.Fatalf("expansion field 4 not reflected in statstring charflags: %#x", flags)
	}
}

func TestClassicCreateLeavesCharflagsUnset(t *testing.T) {
	_, datas := decodeTestFields(t, characterSummary(d2rCharacter{
		Name: "Hansolo", Class: 4, Flags: 0, Field4: 0, ID: 1, Level: 1,
	}))
	raw := datas[3]
	flags := uint64(raw[26]&0x7f) | uint64(raw[27]&0x7f)<<7
	if flags != 0 {
		t.Fatalf("classic character must not gain flags: %#x", flags)
	}
}

func TestCharacterSummaryUsesStoredValues(t *testing.T) {
	c := d2rCharacter{Name: "Mage", Class: 1, Flags: 3, ID: 9, Level: 5}
	_, datas := decodeTestFields(t, characterSummary(c))
	raw := datas[3]
	flags := uint64(raw[26]&0x7f) | uint64(raw[27]&0x7f)<<7
	if raw[13] != 2 || raw[25] != 5 || flags != 3 {
		t.Fatalf("stored class/level/flags not reflected: %d/%d/%#x", raw[13], raw[25], flags)
	}
}

func TestCharacterJSONExtraOverride(t *testing.T) {
	c := d2rCharacter{Name: "Hansolo", Class: 4, Flags: 0, Field4: 2, ID: 2, Level: 1}

	base := characterJSONData(c, nil)
	var baseDoc map[string]any
	if err := json.Unmarshal(base, &baseDoc); err != nil {
		t.Fatalf("base doc: %v", err)
	}
	if baseDoc["version"] != float64(0) {
		t.Fatalf("base version should stay 0, got %s", base)
	}
	if baseDoc["charflags"] != float64(0x20) {
		t.Fatalf("base charflags should carry the expansion bit, got %s", base)
	}

	tune := t.TempDir() + "/character.json"
	if err := os.WriteFile(tune, []byte(`{"_format":"json","version":5,"acts":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATTLENET_CHARACTER_JSON_TUNE", tune)
	merged := characterData(c)
	var mergedDoc map[string]any
	if err := json.Unmarshal(merged, &mergedDoc); err != nil {
		t.Fatalf("merged doc: %v", err)
	}
	if mergedDoc["version"] != float64(5) {
		t.Fatalf("extra should override version, got %s", merged)
	}
	if acts, ok := mergedDoc["acts"].([]any); !ok || len(acts) != 0 {
		t.Fatalf("extra should add acts, got %s", merged)
	}
	if mergedDoc["class"] != float64(4) {
		t.Fatalf("extra must not clobber stored class, got %s", merged)
	}
}

func TestCharacterDataFormatProbe(t *testing.T) {
	c := d2rCharacter{Name: "Hansolo", Class: 4, Flags: 0, Field4: 2, ID: 2, Level: 1}
	tune := t.TempDir() + "/character.json"
	t.Setenv("BATTLENET_CHARACTER_JSON_TUNE", tune)

	if err := os.WriteFile(tune, []byte(`{"_format":"legacy-empty"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := characterData(c); len(got) != 0 {
		t.Fatalf("legacy-empty payload = %x, want empty", got)
	}

	if err := os.WriteFile(tune, []byte(`{"_format":"legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := characterData(c)
	if len(got) != 31 {
		t.Fatalf("legacy payload length = %d, want 31", len(got))
	}
	if got[13] != 5 || got[25] != 1 {
		t.Fatalf("legacy class/level = %d/%d, want 5/1", got[13], got[25])
	}
	if flags := uint64(got[26]&0x7f) | uint64(got[27]&0x7f)<<7; flags != 0x20 {
		t.Fatalf("legacy charflags = %#x, want %#x", flags, uint64(0x20))
	}
}

func TestLevelOneCharactersKeepDistinctIDs(t *testing.T) {
	a := d2rCharacter{Name: "First", Class: 4, Flags: 0, ID: 1, Level: 1}
	b := d2rCharacter{Name: "Second", Class: 1, Flags: 0, ID: 2, Level: 1}
	va, _ := decodeTestFields(t, characterSummary(a))
	vb, _ := decodeTestFields(t, characterSummary(b))
	if va[1] == vb[1] {
		t.Fatalf("distinct characters share summary field 1 = %d", va[1])
	}
	if va[1] != 1 || vb[1] != 2 {
		t.Fatalf("summary IDs = %d/%d, want 1/2", va[1], vb[1])
	}
}

func TestSummaryRoundTripsThroughStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATTLENET_CHARACTER_STORE", dir+"/characters.json")

	charMu.Lock()
	charList = nil
	charNext = 1
	charMu.Unlock()

	before, ok := handleCharacterRPC(&gatewaySession{remote: "test"}, d2rHeader{
		service: characterHash,
		method:  charCreate,
		body:    append(append(pbBytes(1, []byte("Sorceress")), pbVarint(2, 1)...), pbVarint(3, 2)...),
	})
	if !ok {
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
	if len(charList) != 1 {
		t.Fatalf("roster after restart: %+v", charList)
	}
	got := charList[0]
	if got.Name != "Sorceress" || got.Class != 1 || got.Flags != 2 || got.ID != 1 || got.Level != 1 {
		t.Fatalf("fields lost across save/load: %+v", got)
	}
	if !bytes.Equal(characterSummary(got), characterSummary(d2rCharacter{
		Name: "Sorceress", Class: 1, Flags: 2, ID: 1, Level: 1,
	})) {
		t.Fatal("reloaded fields do not reproduce the same summary")
	}
	_ = before
}

func TestDeleteCharacter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATTLENET_CHARACTER_STORE", dir+"/characters.json")

	charMu.Lock()
	charList = nil
	charNext = 1
	charMu.Unlock()

	ses := &gatewaySession{remote: "test"}
	create := append(append(pbBytes(1, []byte("Hansolo")), pbVarint(2, 4)...), pbVarint(3, 0)...)
	if _, ok := handleCharacterRPC(ses, d2rHeader{service: characterHash, method: charCreate, body: create}); !ok {
		t.Fatal("CreateCharacter not handled")
	}
	// Live client bytes for deleting character 1: 08 01 10 00.
	body, ok := handleCharacterRPC(ses, d2rHeader{
		service: characterHash, method: charDelete, body: []byte{0x08, 0x01, 0x10, 0x00},
	})
	if !ok {
		t.Fatal("DeleteCharacter not handled")
	}
	if len(body) != 0 {
		t.Fatalf("delete response should be empty, got %x", body)
	}
	charMu.Lock()
	defer charMu.Unlock()
	if len(charList) != 0 {
		t.Fatalf("roster after delete: %+v", charList)
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
