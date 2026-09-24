package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

// Character service (init 0x16c31d4). The client creates a character and then
// asks for its list; until both are answered with real bodies the character
// screen stays empty.
const (
	characterHash      = uint32(0x607fd196)
	charCreate         = uint32(0x043afeb5)
	charDelete         = uint32(0xa1d52098)
	charReadList       = uint32(0x47821811)
	charUse            = uint32(0x6a706ac6)
	gameManagementHash = uint32(0x110f2741)
	gameGetSitePings   = uint32(0xbb6dcaa4)
)

// A character as the title sends it: CreateCharacterRequest is
// {1 string name, 2 varint class, 3 varint flags, 4 varint kind} — read off
// the wire. A classic create sent 0a07 "Hansolo" 10 04 18 00 (no field 4);
// an expansion create sent 0a07 "Hansolo" 10 04 18 00 20 02 (field 4 = 2).
// Field 4's name is unsettled; its value is preserved verbatim.
type d2rCharacter struct {
	Name   string
	Class  uint64
	Flags  uint64
	Field4 uint64
	ID     uint64
	Level  uint64
}

// One roster for the whole box, mirrored to disk. It began as process memory
// with a note to persist it "when a second account exists"; what actually
// forced the issue was deploys — every service restart wiped the character and
// the player had to make a new one before they could test anything.
//
// ponytail: one JSON file, rewritten whole on each change. A roster is a
// handful of entries written once per character creation; reach for a real
// store when something writes it per-frame.
var (
	charMu   sync.Mutex
	charList []d2rCharacter
	charNext uint64 = 1
)

func charStorePath() string {
	return env("BATTLENET_CHARACTER_STORE", "/var/lib/battlenet/characters.json")
}

type charStore struct {
	Next       uint64         `json:"next_id"`
	Characters []d2rCharacter `json:"characters"`
}

// loadCharacters restores the roster at startup. A missing file is the normal
// first run, not an error.
func loadCharacters() {
	path := charStorePath()
	blob, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("CHARACTERS: reading %s: %v", path, err)
			return
		}
		// First run. Write the empty roster now rather than at the first
		// character creation: the volume was once mounted unwritable by the
		// container user and the only sign was one line in the log at create
		// time, by which point the player had already lost the character.
		charMu.Lock()
		saveCharactersLocked()
		charMu.Unlock()
		return
	}
	var st charStore
	if err := json.Unmarshal(blob, &st); err != nil {
		log.Printf("CHARACTERS: parsing %s: %v", path, err)
		return
	}

	charMu.Lock()
	defer charMu.Unlock()
	charList = st.Characters
	charNext = st.Next
	if charNext == 0 {
		charNext = 1
	}
	log.Printf("CHARACTERS: restored %d from %s", len(charList), path)
}

// saveCharactersLocked writes the roster. Call with charMu held. Writes to a
// temporary file and renames, so a crash mid-write cannot leave a half-written
// roster in place of a good one.
func saveCharactersLocked() {
	path := charStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("CHARACTERS: creating %s: %v", filepath.Dir(path), err)
		return
	}
	blob, err := json.Marshal(charStore{Next: charNext, Characters: charList})
	if err != nil {
		log.Printf("CHARACTERS: encoding roster: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		log.Printf("CHARACTERS: writing %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("CHARACTERS: renaming onto %s: %v", path, err)
		return
	}
	log.Printf("CHARACTERS: saved %d to %s", len(charList), path)
}

// parseCreateCharacter reads the request the client actually sends.
func parseCreateCharacter(body []byte) d2rCharacter {
	var c d2rCharacter
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			break
		}
		body = body[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return c
			}
			c.Name = string(v)
			body = body[m:]
		case num == 2 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return c
			}
			c.Class = v
			body = body[m:]
		case num == 3 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return c
			}
			c.Flags = v
			body = body[m:]
		case num == 4 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return c
			}
			c.Field4 = v
			body = body[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, body)
			if m < 0 {
				return c
			}
			body = body[m:]
		}
	}
	return c
}

// characterSummary encodes classic.protocol.external.v1.character.CharacterSummary.
// Layout from the LITE parser at text 0x16c0ea0, cross-checked against the
// serializer at 0x16c11f4: field 1 varint (obj+0x38), field 2 length-delimited
// through the bytes helper 0x105bd4c (obj+0x40), field 3 length-delimited
// through the UTF-8-checked string helper 0x105be5c (obj+0x48).
// Field assignment was first narrowed by two live renders:
//
//	field 2 = "Hansolo", field 3 = "Hansolo"  -> the name appeared
//	field 2 = empty,     field 3 = "Hansolo"  -> the name was GONE
//
// The 2026-09-21 consumer trace settled all three fields: field 1 is the
// character ID, field 2 is the name, and field 3 is the character-data string.
// The runtime builder treats field 3 shorter than 51 bytes as a legacy binary
// statstring and otherwise parses JSON containing class, level, flags,
// appearance and progression.
func characterSummary(c d2rCharacter) []byte {
	out := pbVarint(1, c.ID)
	out = append(out, pbBytes(2, []byte(c.Name))...)
	return append(out, pbBytes(3, characterData(c))...)
}

// characterData emits the fixed-offset statstring used by the working online
// character-select path. A live A/B test established that the JSON decoder
// renders metadata but changes the online quick action to New Character and
// loses the Classic/Expansion presentation; the populated legacy decoder
// renders the model and metadata while preserving Play and the badges.
// Research-only formats remain hot-switchable without a server restart.
func characterData(c d2rCharacter) []byte {
	extra := characterJSONExtra()
	if raw, ok := extra["_format"]; ok {
		var format string
		if json.Unmarshal(raw, &format) == nil {
			switch format {
			case "legacy-empty":
				return nil
			case "json":
				delete(extra, "_format")
				return characterJSONData(c, extra)
			}
		}
	}
	return characterLegacyData(c)
}

// characterJSONData retains the alternate >=51-byte decoder as a diagnostic
// surface. It is not the production encoding; select it explicitly with
// {"_format":"json"} in the tune file.
func characterJSONData(c d2rCharacter, extra map[string]json.RawMessage) []byte {
	flags := c.Flags
	if c.Field4 == 2 {
		flags |= 0x20
	}
	doc, err := json.Marshal(struct {
		Version    uint64   `json:"version"`
		Appearance []uint64 `json:"appearance"`
		Class      uint64   `json:"class"`
		Level      uint64   `json:"level"`
		CharFlags  uint64   `json:"charflags"`
	}{
		Version:    0,
		Appearance: []uint64{},
		Class:      c.Class,
		Level:      c.Level,
		CharFlags:  flags,
	})
	if err != nil {
		return []byte("{}")
	}
	if len(extra) > 0 {
		var m map[string]any
		if err := json.Unmarshal(doc, &m); err != nil {
			return doc
		}
		for k, v := range extra {
			var val any
			if err := json.Unmarshal(v, &val); err != nil {
				continue
			}
			m[k] = val
		}
		if merged, err := json.Marshal(m); err == nil {
			return merged
		}
	}
	return doc
}

// characterLegacyData builds the fixed-offset character statstring consumed
// when field 3 is shorter than 51 bytes. The offsets come directly from the
// client's legacy branch: appearance 2..12, class+1 at 13, level at 25,
// 7-bit-split charflags at 26/27, and current act at 30. Zero appearance
// components describe an unequipped character while remaining valid UTF-8.
func characterLegacyData(c d2rCharacter) []byte {
	data := make([]byte, 31)
	data[13] = byte(c.Class + 1)
	data[25] = byte(c.Level)
	flags := c.Flags
	if c.Field4 == 2 {
		flags |= 0x20
	}
	data[26] = byte(flags & 0x7f)
	data[27] = byte((flags >> 7) & 0x7f)
	return data
}

// characterJSONExtra returns optional research overrides for field 3. The
// reserved _format key selects "legacy-empty" or "json"; absent, unknown and
// "legacy" values all keep the production legacy statstring.
//
// This is a probe surface for the playable-marking keys (acts, equipment,
// questRecords and friends are recognized by the client but their shapes are
// not settled, so they are not baked into the code). The file is re-read on
// every ReadCharacterList, which the title re-requests each time the player
// backs out and re-enters the character screen — so each edit takes effect
// with no restart and no dropped session. Fold the settled keys into
// characterData and drop this knob when the remaining probes conclude.
func characterJSONExtra() map[string]json.RawMessage {
	path := env("BATTLENET_CHARACTER_JSON_TUNE", "/var/lib/battlenet/character.json")
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		log.Printf("CHARACTERS: ignoring invalid %s: %v", path, err)
		return nil
	}
	return m
}

// readCharacterListBody encodes the roster.
//
// ponytail: the carrier is the message whose parser is 0x16c186c —
// {1 varint, 2 repeated CharacterSummary}, element merge `bl 0x16c0ea0`,
// vtable 0x1ec7df8. ReadCharacterListResponse's own block (GetTypeName
// 0x16c2500, parser 0x16c2740) dispatches ONLY field 1, so either 0x16c186c
// is the real response class or the list arrives some other way. Sending both
// shapes at once costs two fields and settles it on the wire: if characters
// appear, 0x16c186c is the response.
func readCharacterListBody() []byte {
	charMu.Lock()
	defer charMu.Unlock()

	out := pbVarint(1, 0)
	for _, c := range charList {
		out = append(out, pbBytes(2, characterSummary(c))...)
	}
	return out
}

// createCharacterBody encodes CreateCharacterResponse. The success adapter
// forwards only field 1, the new character's uint64 ID.
func createCharacterBody(c d2rCharacter) []byte {
	return pbVarint(1, c.ID)
}

// parseDeleteCharacter reads DeleteCharacterRequest. The live client sent
// 08 01 10 00 for character 1: field 1 is the character ID (field 2 carries
// a zero value the server ignores).
func parseDeleteCharacter(body []byte) uint64 {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			break
		}
		body = body[n:]
		if num == 1 && typ == protowire.VarintType {
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return 0
			}
			return v
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			return 0
		}
		body = body[m:]
	}
	return 0
}

// handleCharacterRPC answers the Character service. Returns false when the
// method is not ours, so the caller falls back to the empty-body answer.
func handleCharacterRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case charCreate:
		c := parseCreateCharacter(h.body)
		charMu.Lock()
		c.ID = charNext
		c.Level = 1 // a fresh character; the title never tells us a level
		charNext++
		charList = append(charList, c)
		count := len(charList)
		saveCharactersLocked()
		charMu.Unlock()
		log.Printf("D2RSVC %s: CreateCharacter %q class=%d flags=%d field4=%d id=%d (%d on the roster)",
			s.remote, c.Name, c.Class, c.Flags, c.Field4, c.ID, count)
		return createCharacterBody(c), true
	case charReadList:
		body := readCharacterListBody()
		charMu.Lock()
		count := len(charList)
		charMu.Unlock()
		log.Printf("D2RSVC %s: ReadCharacterList -> %d character(s), %d-byte body", s.remote, count, len(body))
		return body, true
	case charDelete:
		id := parseDeleteCharacter(h.body)
		charMu.Lock()
		kept := charList[:0]
		removed := false
		for _, c := range charList {
			if c.ID == id {
				removed = true
				continue
			}
			kept = append(kept, c)
		}
		charList = kept
		if removed {
			saveCharactersLocked()
		}
		count := len(charList)
		charMu.Unlock()
		log.Printf("D2RSVC %s: DeleteCharacter id=%d removed=%v (%d on the roster)",
			s.remote, id, removed, count)
		return []byte{}, true
	}
	return nil, false
}
