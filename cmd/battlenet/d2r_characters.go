package main

import (
	"log"
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
// {1 string name, 2 varint class, 3 varint flags} — read off the wire, the
// live client sent 0a07 "Hansolo" 10 04 18 00.
type d2rCharacter struct {
	Name  string
	Class uint64
	Flags uint64
	ID    uint64
}

// ponytail: process memory, one roster for the whole box. This is a single
// dev console talking to a single account; give it a store per subject (and
// something on disk) when a second account exists.
var (
	charMu   sync.Mutex
	charList []d2rCharacter
	charNext uint64 = 1
)

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
// through the UTF-8-checked string helper 0x105be5c (obj+0x48). Only field 3
// is certainly text, so the name goes there; field 1 carries the class, which
// is the one varint the client itself sent us.
// Field 2 is NOT text. Sent the name there once and the select screen drew
// "Hansolo LEVEL 0 80 Warriv's All Right" -- the title parsed those bytes as
// character progress and rendered an act/quest string out of them. Left empty
// until its real shape is known; the name lives in field 3, which is the
// UTF-8-checked one and renders correctly.
func characterSummary(c d2rCharacter) []byte {
	out := pbVarint(1, c.Class)
	out = append(out, pbBytes(2, nil)...)
	return append(out, pbBytes(3, []byte(c.Name))...)
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

// createCharacterBody encodes CreateCharacterResponse: parser 0x16be9c8,
// field 1 a 64-bit varint (obj+0x38) and field 2 a 32-bit varint (obj+0x40).
// Field 1 is answered with the new character's id and field 2 with 0, the
// value every other status field on this connection uses for success.
func createCharacterBody(c d2rCharacter) []byte {
	return append(pbVarint(1, c.ID), pbVarint(2, 0)...)
}

// handleCharacterRPC answers the Character service. Returns false when the
// method is not ours, so the caller falls back to the empty-body answer.
func handleCharacterRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case charCreate:
		c := parseCreateCharacter(h.body)
		charMu.Lock()
		c.ID = charNext
		charNext++
		charList = append(charList, c)
		count := len(charList)
		charMu.Unlock()
		log.Printf("D2RSVC %s: CreateCharacter %q class=%d flags=%d id=%d (%d on the roster)",
			s.remote, c.Name, c.Class, c.Flags, c.ID, count)
		return createCharacterBody(c), true
	case charReadList:
		body := readCharacterListBody()
		charMu.Lock()
		count := len(charList)
		charMu.Unlock()
		log.Printf("D2RSVC %s: ReadCharacterList -> %d character(s), %d-byte body", s.remote, count, len(body))
		return body, true
	}
	return nil, false
}
