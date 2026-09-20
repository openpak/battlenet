package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"os"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	gameCreate    = uint32(0x930fbb99)
	gameJoin      = uint32(0xa5cc1951)
	gameDetail    = uint32(0xb5cd1354)
	gameListAll   = uint32(0xa5ea9f6d)
	gameCreateCan = uint32(0x38274117)
)

// gameDetailName reads GameDetailRequest {1: string name}; the live client
// sends 0a0e followed by the 14-character game name.
func gameDetailName(body []byte) string {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return ""
		}
		body = body[n:]
		if num == 1 && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return ""
			}
			return string(v)
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			return ""
		}
		body = body[m:]
	}
	return ""
}

// GameDetailResponse, from the parser's dispatch at 0x16f8100..0x16f8260:
//
//	1 bytes  (+0x38)   2 bytes  (+0x40)   5 bytes  (+0x50)
//	3,4,6,7,8 uint32
//	9 repeated message (array +0x60, count +0x68, cap +0x70)
//
// Probing this needs many small experiments: the first pass put marker
// addresses in fields 2 and 5 and the client dialled neither, but every varint
// was 0 and one of them is very likely a game-state flag, so the strings were
// never going to be acted on. Sweeping five uint32s a deploy at a time costs a
// dropped session and a manual re-entry into Online each time.
//
// So the body is tunable from a file on the persistent volume, re-read on every
// request. Editing it changes the next answer with no restart and no dropped
// session. ponytail: a few hundred bytes read per RPC at one request every 3 s
// -- when this stops being a probe, fold the settled values into code and drop
// the file.
type gameDetailTune struct {
	Field1  *string `json:"field1"` // nil = echo the requested game name
	Field2  string  `json:"field2"`
	Field5  string  `json:"field5"`
	Field3  uint64  `json:"field3"`
	Field4  uint64  `json:"field4"`
	Field6  uint64  `json:"field6"`
	Field7  uint64  `json:"field7"`
	Field8  uint64  `json:"field8"`
	Players int     `json:"players"` // how many empty GameDetailPlayer entries in field 9

	// GameCreateResponse and GameJoinResponse bodies, as raw hex. They are
	// one-shot replies -- the client gets each exactly once and only then
	// starts polling GameDetail -- so they are the likelier gate, and they
	// cannot be probed by repetition the way GameDetail can. Hex rather than
	// named fields because their meanings are not settled. GameJoinResponse's
	// wire layout is settled; notably field 6 is a bool, while the other
	// varints are uint32 values (field 2 is narrowed to 16 bits by the result
	// adapter).
	CreateHex string `json:"create_hex"`
	JoinHex   string `json:"join_hex"`
}

func loadGameDetailTune() gameDetailTune {
	var t gameDetailTune
	path := env("BATTLENET_GAMEDETAIL_TUNE", "/var/lib/battlenet/gamedetail.json")
	blob, err := os.ReadFile(path)
	if err != nil {
		return t
	}
	if err := json.Unmarshal(blob, &t); err != nil {
		log.Printf("GAMEDETAIL: parsing %s: %v", path, err)
		return gameDetailTune{}
	}
	return t
}

func gameDetailBody(name string) ([]byte, gameDetailTune) {
	t := loadGameDetailTune()

	first := name
	if t.Field1 != nil {
		first = *t.Field1
	}

	out := pbBytes(1, []byte(first))
	if t.Field2 != "" {
		out = append(out, pbBytes(2, []byte(t.Field2))...)
	}
	if t.Field3 != 0 {
		out = append(out, pbVarint(3, t.Field3)...)
	}
	if t.Field4 != 0 {
		out = append(out, pbVarint(4, t.Field4)...)
	}
	if t.Field5 != "" {
		out = append(out, pbBytes(5, []byte(t.Field5))...)
	}
	if t.Field6 != 0 {
		out = append(out, pbVarint(6, t.Field6)...)
	}
	if t.Field7 != 0 {
		out = append(out, pbVarint(7, t.Field7)...)
	}
	if t.Field8 != 0 {
		out = append(out, pbVarint(8, t.Field8)...)
	}
	for i := 0; i < t.Players; i++ {
		out = append(out, pbBytes(9, nil)...)
	}
	return out, t
}

// handleGameRPC answers GameManagement. Returns false for methods that are not
// ours, so the caller falls back to the empty-body answer.
func handleGameRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case gameDetail:
		name := gameDetailName(h.body)
		body, t := gameDetailBody(name)
		log.Printf("D2RSVC %s: GameDetail %q -> %d bytes  f2=%q f5=%q f3=%d f4=%d f6=%d f7=%d f8=%d players=%d",
			s.remote, name, len(body), t.Field2, t.Field5, t.Field3, t.Field4, t.Field6, t.Field7, t.Field8, t.Players)
		return body, true
	case gameCreate, gameJoin:
		t := loadGameDetailTune()
		raw := t.CreateHex
		what := "GameCreate"
		if h.method == gameJoin {
			raw, what = t.JoinHex, "GameJoin"
		}
		if raw == "" {
			return nil, false
		}
		body, err := hex.DecodeString(raw)
		if err != nil {
			log.Printf("D2RSVC %s: %s hex is not decodable: %v", s.remote, what, err)
			return nil, false
		}
		log.Printf("D2RSVC %s: %s -> tuned body %d bytes (%s)", s.remote, what, len(body), raw)
		return body, true
	}
	return nil, false
}
