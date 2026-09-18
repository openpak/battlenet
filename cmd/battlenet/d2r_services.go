package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"log"

	"google.golang.org/protobuf/encoding/protowire"
)

// The D2R-services WebSocket: the title opens it at LoginResponse.field1 +
// "/OSI/v2/rpc/client" once it goes online. One RPC per binary message:
// [u16 BE header length][classic.protocol.Header][payload]. Header fields:
// 1 service hash, 2 method hash, 3 request token (responses are matched on
// it, and on 1+2 for the response parser), 4 client check value, 5 payload
// size, 6 status (0 = OK), 9 is_response, 12 trace label (unread).
const (
	d2rServicesHash = uint32(0xeeb6ab22) // D2rServicesConnection
	d2rAuthSession  = uint32(0xed028f6e)
)

type d2rHeader struct {
	service, method, token uint32
	body                   []byte
}

func parseD2RHeader(packet []byte) (d2rHeader, bool) {
	var h d2rHeader
	if len(packet) < 2 {
		return h, false
	}
	n := int(binary.BigEndian.Uint16(packet))
	if len(packet)-2 < n {
		return h, false
	}
	for b := packet[2 : 2+n]; len(b) > 0; {
		num, typ, m := protowire.ConsumeTag(b)
		if m < 0 {
			return h, false
		}
		b = b[m:]
		if typ == protowire.VarintType {
			v, k := protowire.ConsumeVarint(b)
			if k < 0 {
				return h, false
			}
			switch num {
			case 1:
				h.service = uint32(v)
			case 2:
				h.method = uint32(v)
			case 3:
				h.token = uint32(v)
			}
			b = b[k:]
			continue
		}
		k := protowire.ConsumeFieldValue(num, typ, b)
		if k < 0 {
			return h, false
		}
		b = b[k:]
	}
	h.body = packet[2+n:]
	return h, true
}

// d2rResponse answers a request: service, method and token echoed (the
// client picks the response parser by service+method), status 0,
// is_response set, payload size in field 5.
func d2rResponse(h d2rHeader, body []byte) []byte {
	header := pbVarint(1, uint64(h.service))
	header = append(header, pbVarint(2, uint64(h.method))...)
	header = append(header, pbVarint(3, uint64(h.token))...)
	header = append(header, pbVarint(5, uint64(len(body)))...)
	header = append(header, pbVarint(9, 1)...)
	out := make([]byte, 2, 2+len(header)+len(body))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, body...)
}

// handleD2RServices answers every request with status 0. AuthSession gets an
// AuthSessionResponse carrying the two fields the client reads at once
// (9: session id, 10: 0); anything else (next: GameVersion.SetGameVersion,
// whose response is classic.protocol.Empty) gets an empty body.
func handleD2RServices(s *gatewaySession, payload []byte) bool {
	h, ok := parseD2RHeader(payload)
	if !ok {
		log.Printf("D2RSVC %s: unparsable frame %x", s.remote, payload)
		return true
	}
	log.Printf("D2RSVC %s rpc service=%#08x method=%#08x token=%d body=%x",
		s.remote, h.service, h.method, h.token, h.body)
	var body []byte
	if h.service == d2rServicesHash && h.method == d2rAuthSession {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			log.Printf("D2RSVC %s: session id entropy: %v", s.remote, err)
			return false
		}
		body = pbBytes(9, []byte(hex.EncodeToString(id)))
		body = append(body, pbVarint(10, 0)...)
	}
	s.send(d2rResponse(h, body))
	log.Printf("D2RSVC %s: answered token %d (%d-byte body)", s.remote, h.token, len(body))
	return true
}
