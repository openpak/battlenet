package main

import (
	"log"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	gameCreate    = uint32(0x930fbb99)
	gameJoin      = uint32(0xa5cc1951)
	gameDetail    = uint32(0xb5cd1354)
	gameListAll   = uint32(0xa5ea9f6d)
	gameCreateCan = uint32(0x38274117)
)

// GameDetailResponse, from the parser's dispatch at 0x16f8100..0x16f8260:
//
//	1 bytes  (+0x38)   2 bytes  (+0x40)   5 bytes  (+0x50)
//	3,4,6,7,8 uint32
//	9 repeated message (array +0x60, count +0x68, cap +0x70)
//
// One of the three string fields has to name the host, because the title is
// peer-hosted and a joining client must dial a GameMaster. Which one is not
// decidable from the parser: all three go through the same bytes helper.
//
// So this is a PROBE, not an answer. Each string field carries a different
// marker address; whichever one the client dials identifies the field, and the
// dial shows up in the emulator's socket log as a connect to that exact IP.
// The varints stay 0 so the only thing varying is the strings. Either outcome
// is informative: a dial names the field, no dial means the host address is
// not in GameDetail at all and GameJoinResponse is next.
const (
	probeHostField2 = "10.90.90.92:6112"
	probeHostField5 = "10.90.90.95:6112"
)

var (
	gameMu    sync.Mutex
	gameNames = map[string]bool{}
)

// gameDetailName reads GameDetailRequest {1: string name}.
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

func gameDetailBody(name string) []byte {
	out := pbBytes(1, []byte(name))
	out = append(out, pbBytes(2, []byte(probeHostField2))...)
	out = append(out, pbBytes(5, []byte(probeHostField5))...)
	return out
}

// handleGameRPC answers GameManagement. Returns false for methods that are not
// ours, so the caller falls back to the empty-body answer.
func handleGameRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case gameDetail:
		name := gameDetailName(h.body)
		body := gameDetailBody(name)
		log.Printf("D2RSVC %s: GameDetail %q -> probe body %d bytes (field2=%s field5=%s)",
			s.remote, name, len(body), probeHostField2, probeHostField5)
		return body, true
	case gameCreate, gameJoin:
		// Left empty on purpose: the client already walks past both to
		// GameDetail, so changing them at the same time would blur which
		// response moved the client.
		return nil, false
	}
	return nil, false
}
