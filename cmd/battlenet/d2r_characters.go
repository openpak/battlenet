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

// One roster per account, mirrored to disk. It began as process memory with a
// note to persist it "when a second account exists"; what actually forced the
// persistence was deploys -- every service restart wiped the character and the
// player had to make a new one before they could test anything.
//
// Rosters are keyed by the battlenet subject the D2R-services session proved in
// AuthSession (d2rTokenSubject: the signed LoginResponse field 2 handed back),
// never by address. Every console reaches us through Traefik, so two players
// on one LAN, or two emulator instances on one PC, arrive from the same
// address; until v0.2.0 there was a single roster for the whole box and a
// second account saw (and could play or delete) the first one's characters.
// Character IDs stay unique across the box (one counter) because GameJoin
// names a character by ID alone.
//
// ponytail: one JSON file, rewritten whole on each change. A roster is a
// handful of entries written once per character creation; reach for a real
// store when something writes it per-frame.
var (
	charMu      sync.Mutex
	charRosters        = map[string][]d2rCharacter{} // battlenet subject -> roster
	charNext    uint64 = 1
	// charUnowned holds a v0.2.0 store's single roster until an owner is
	// named (BATTLENET_CHARACTER_LEGACY_OWNER). It is kept on disk and served
	// to nobody: guessing the owner is how the leak happened in the first place.
	charUnowned []d2rCharacter
)

func charStorePath() string {
	return env("BATTLENET_CHARACTER_STORE", "/var/lib/battlenet/characters.json")
}

type charStore struct {
	Next uint64 `json:"next_id"`
	// Characters is the v0.2.0 box-wide roster. Read for migration; written
	// back only while it still has no owner.
	Characters []d2rCharacter            `json:"characters,omitempty"`
	Accounts   map[string][]d2rCharacter `json:"accounts"`
}

// loadCharacters restores the rosters at startup. A missing file is the normal
// first run, not an error.
func loadCharacters() {
	path := charStorePath()
	blob, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("CHARACTERS: reading %s: %v", path, err)
			return
		}
		// First run. Write the empty store now rather than at the first
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
	charRosters = st.Accounts
	if charRosters == nil {
		charRosters = map[string][]d2rCharacter{}
	}
	charUnowned = st.Characters
	charNext = st.Next
	if charNext == 0 {
		charNext = 1
	}
	if len(charUnowned) > 0 {
		if owner := strings.TrimSpace(os.Getenv("BATTLENET_CHARACTER_LEGACY_OWNER")); owner != "" {
			charRosters[owner] = append(charRosters[owner], charUnowned...)
			log.Printf("CHARACTERS: assigned %d legacy character(s) to BATTLENET_CHARACTER_LEGACY_OWNER", len(charUnowned))
			charUnowned = nil
			saveCharactersLocked()
		} else {
			log.Printf("CHARACTERS: %d legacy character(s) from the shared roster have no owner and are served to nobody; set BATTLENET_CHARACTER_LEGACY_OWNER to the owner's battlenet subject to restore them", len(charUnowned))
		}
	}
	log.Printf("CHARACTERS: restored %d account roster(s), %d character(s) from %s", len(charRosters), countCharactersLocked(), path)
}

// countCharactersLocked counts every owned character. Call with charMu held.
func countCharactersLocked() int {
	n := 0
	for _, r := range charRosters {
		n += len(r)
	}
	return n
}

// saveCharactersLocked writes the rosters. Call with charMu held. Writes to a
// temporary file and renames, so a crash mid-write cannot leave a half-written
// store in place of a good one.
func saveCharactersLocked() {
	path := charStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("CHARACTERS: creating %s: %v", filepath.Dir(path), err)
		return
	}
	blob, err := json.Marshal(charStore{Next: charNext, Characters: charUnowned, Accounts: charRosters})
	if err != nil {
		log.Printf("CHARACTERS: encoding rosters: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		log.Printf("CHARACTERS: writing %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("CHARACTERS: renaming onto %s: %v", path, err)
		return
	}
	log.Printf("CHARACTERS: saved %d character(s) in %d account roster(s) to %s", countCharactersLocked(), len(charRosters), path)
}

// accountCharacters returns a copy of one account's roster.
func accountCharacters(subject string) []d2rCharacter {
	charMu.Lock()
	defer charMu.Unlock()
	return append([]d2rCharacter(nil), charRosters[subject]...)
}

// addCharacter stores a new character on its owner's roster, handing out the
// box-wide ID. It returns the stored character and that roster's size.
func addCharacter(subject string, c d2rCharacter) (d2rCharacter, int) {
	charMu.Lock()
	defer charMu.Unlock()
	c.ID = charNext
	c.Level = 1 // a fresh character; the title never tells us a level
	charNext++
	charRosters[subject] = append(charRosters[subject], c)
	saveCharactersLocked()
	return c, len(charRosters[subject])
}

// removeCharacter deletes a character from its owner's roster only: an ID on
// another account's roster is not found.
func removeCharacter(subject string, id uint64) (removed bool, left int) {
	charMu.Lock()
	defer charMu.Unlock()
	var kept []d2rCharacter
	for _, c := range charRosters[subject] {
		if c.ID == id {
			removed = true
			continue
		}
		kept = append(kept, c)
	}
	if !removed {
		return false, len(charRosters[subject])
	}
	if len(kept) == 0 {
		delete(charRosters, subject)
	} else {
		charRosters[subject] = kept
	}
	saveCharactersLocked()
	return true, len(kept)
}

// findCharacter looks a character up on its owner's roster.
func findCharacter(subject string, id uint64) (d2rCharacter, bool) {
	charMu.Lock()
	defer charMu.Unlock()
	for _, c := range charRosters[subject] {
		if c.ID == id {
			return c, true
		}
	}
	return d2rCharacter{}, false
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
func readCharacterListBody(roster []d2rCharacter) []byte {
	out := pbVarint(1, 0)
	for _, c := range roster {
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

// handleCharacterRPC answers the Character service for the session's account.
// Returns false when the method is not ours, so the caller falls back to the
// empty-body answer. A session that never proved an account (no AuthSession,
// or a token that does not verify) has no roster: it reads an empty list and
// cannot create or delete. Character names never reach the log.
func handleCharacterRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case charCreate:
		c := parseCreateCharacter(h.body)
		if s.subject == "" {
			log.Printf("D2RSVC %s: CreateCharacter refused: session has no account", s.remote)
			return []byte{}, true
		}
		c, count := addCharacter(s.subject, c)
		log.Printf("D2RSVC %s: CreateCharacter class=%d flags=%d field4=%d id=%d (%d on this account's roster)",
			s.remote, c.Class, c.Flags, c.Field4, c.ID, count)
		return createCharacterBody(c), true
	case charReadList:
		var roster []d2rCharacter
		if s.subject != "" {
			roster = accountCharacters(s.subject)
		}
		body := readCharacterListBody(roster)
		log.Printf("D2RSVC %s: ReadCharacterList -> %d character(s), %d-byte body", s.remote, len(roster), len(body))
		return body, true
	case charDelete:
		id := parseDeleteCharacter(h.body)
		removed, count := false, 0
		if s.subject != "" {
			removed, count = removeCharacter(s.subject, id)
		}
		log.Printf("D2RSVC %s: DeleteCharacter id=%d removed=%v (%d on this account's roster)",
			s.remote, id, removed, count)
		return []byte{}, true
	}
	return nil, false
}
