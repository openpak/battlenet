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

	// Remaining D2rServicesConnection methods (stub 0x16de3e4).
	d2rSessionUpdate    = uint32(0x6e824a4f)
	d2rSessionDestroyed = uint32(0xf0652897)
	d2rCookieUpdate     = uint32(0x4ae685fd)
	d2rServerPing       = uint32(0x0644b9e5)
	gameVersionHash     = uint32(0x3d930f0e) // GameVersion
	gameSetGameVersion  = uint32(0xd48de460) // SetGameVersion
)

// d2rMethodName names the D2R-services RPCs we know; unknown pairs log raw.
func d2rMethodName(service, method uint32) string {
	switch {
	case service == d2rServicesHash && method == d2rAuthSession:
		return "D2rServicesConnection.AuthSession"
	case service == d2rServicesHash && method == d2rSessionUpdate:
		return "D2rServicesConnection.SessionUpdate"
	case service == d2rServicesHash && method == d2rSessionDestroyed:
		return "D2rServicesConnection.SessionDestroyed"
	case service == d2rServicesHash && method == d2rCookieUpdate:
		return "D2rServicesConnection.CookieUpdate"
	case service == d2rServicesHash && method == d2rServerPing:
		return "D2rServicesConnection.ServerPing"
	case service == gameVersionHash && method == gameSetGameVersion:
		return "GameVersion.SetGameVersion"
	case service == gameVersionHash && method == gameUpdateRequired:
		return "GameVersion.GameUpdateRequired"
	case service == characterHash:
		switch method {
		case charCreate:
			return "Character.CreateCharacter"
		case charDelete:
			return "Character.DeleteCharacter"
		case charReadList:
			return "Character.ReadCharacterList"
		case charUse:
			return "Character.UseCharacter"
		}
	case service == gameManagementHash:
		switch method {
		case gameCreate:
			return "GameManagement.GameCreate"
		case gameJoin:
			return "GameManagement.GameJoin"
		case gameListAll:
			return "GameManagement.GameList"
		case gameDetail:
			return "GameManagement.GameDetail"
		case gameCreateCan:
			return "GameManagement.GameCreateCancel"
		case gameGetSitePings:
			return "GameManagement.GameGetSitePingList"
		}
	}
	return ""
}

// d2rBodyLoggable reports whether a request body may be logged as hex. The
// AuthSession token, character names and game passwords must not reach the
// log; those requests log their length only.
func d2rBodyLoggable(service, method uint32) bool {
	switch {
	case service == d2rServicesHash && method == d2rAuthSession:
		return false
	case service == characterHash:
		return false
	case service == gameManagementHash && (method == gameCreate || method == gameJoin):
		return false
	}
	return true
}

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

// d2rPush builds a server-initiated request (is_response omitted = 0):
// service + method name the push, token is the server's request id, and
// field 5 carries the body size so the client parses the payload.
func d2rPush(service, method, token uint32, body []byte) []byte {
	header := pbVarint(1, uint64(service))
	header = append(header, pbVarint(2, uint64(method))...)
	header = append(header, pbVarint(3, uint64(token))...)
	header = append(header, pbVarint(5, uint64(len(body)))...)
	out := make([]byte, 2, 2+len(header)+len(body))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, body...)
}

// authSessionResponseBody builds the AuthSessionResponse. The gate at
// 0x162b87c succeeds with the C++ status words +0==0 and +4==0 (verified by
// re-disassembly; earlier notes had the +4 sense inverted), and the
// session-started event then reads field 9 (string session id) and field 10
// (u32). Fields 1-8 stay at their empty defaults — the captured empty-token
// answer is header-only (`0012 08a2d6daf50e 10ee9e8ae80e 1801 2800 4801`,
// i.e. service+method+token=1+size=0+is_response with no body) — so the body
// is just 9+10.
func authSessionResponseBody(sessionID string) []byte {
	body := pbBytes(9, []byte(sessionID))
	return append(body, pbVarint(10, 0)...)
}

// pushSessionState queues the server pushes that follow a successful
// AuthSession on the same connection: SessionUpdate carrying the session,
// an empty CookieUpdate, and a ServerPing. Payload shapes come from the LITE
// layouts (SessionUpdateRequest {1 string, 2 AuthSessionResponse},
// CookieUpdateRequest {1 OfflineCookie}, ServerPingRequest {1 varint}); the
// client dispatches pushes on service+method with is_response=0.
func pushSessionState(s *gatewaySession, sessionID string) {
	update := pbBytes(1, []byte(sessionID))
	update = append(update, pbBytes(2, authSessionResponseBody(sessionID))...)
	s.send(d2rPush(d2rServicesHash, d2rSessionUpdate, 1, update))
	log.Printf("D2RSVC %s: pushed SessionUpdate", s.remote)

	cookie := pbBytes(1, []byte{})
	s.send(d2rPush(d2rServicesHash, d2rCookieUpdate, 2, cookie))
	log.Printf("D2RSVC %s: pushed CookieUpdate", s.remote)

	s.send(d2rPush(d2rServicesHash, d2rServerPing, 3, pbVarint(1, 0)))
	log.Printf("D2RSVC %s: pushed ServerPing", s.remote)
}

// handleD2RServices answers every request with status 0 (omitted). AuthSession
// gets the full AuthSessionResponse skeleton (fields 9+10; 1-8 empty
// defaults) followed by the SessionUpdate/CookieUpdate/ServerPing pushes;
// GameVersion.SetGameVersion (response classic.protocol.Empty) and anything
// else get an empty body — a SetGameVersion failure is only logged by the
// client. Server pushes from the client (SessionDestroyed, CookieUpdate,
// ServerPing responses) are logged and acked empty.
func handleD2RServices(s *gatewaySession, payload []byte) bool {
	h, ok := parseD2RHeader(payload)
	if !ok {
		log.Printf("D2RSVC %s: unparsable frame %x", s.remote, payload)
		return true
	}
	name := d2rMethodName(h.service, h.method)
	if name == "" {
		name = "unknown"
	}
	if d2rBodyLoggable(h.service, h.method) {
		log.Printf("D2RSVC %s rpc %s service=%#08x method=%#08x token=%d body=%x",
			s.remote, name, h.service, h.method, h.token, h.body)
	} else {
		log.Printf("D2RSVC %s rpc %s service=%#08x method=%#08x token=%d body=[%d bytes, not logged]",
			s.remote, name, h.service, h.method, h.token, len(h.body))
	}
	var body []byte
	if h.service == d2rServicesHash && h.method == d2rAuthSession {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			log.Printf("D2RSVC %s: session id entropy: %v", s.remote, err)
			return false
		}
		sessionID := hex.EncodeToString(id)
		s.subject = authSessionSubject(h.body)
		if s.subject == "" {
			// No account: the session stays up (the title tolerates it) but
			// owns no characters and cannot create or join games.
			log.Printf("D2RSVC %s: AuthSession token did not verify; session has no account", s.remote)
		}
		body = authSessionResponseBody(sessionID)
		s.send(d2rResponse(h, body))
		log.Printf("D2RSVC %s: answered AuthSession token %d (%d-byte body)", s.remote, h.token, len(body))
		pushSessionState(s, sessionID)
		return true
	}
	if h.service == gameVersionHash && h.method == gameSetGameVersion {
		s.gameVersion = setGameVersionString(h.body)
	}
	if h.service == characterHash {
		if b, ok := handleCharacterRPC(s, h); ok {
			body = b
		}
	}
	if h.service == gameManagementHash {
		if b, ok := handleGameRPC(s, h); ok {
			body = b
		}
	}
	s.send(d2rResponse(h, body))
	log.Printf("D2RSVC %s: answered %s token %d (%d-byte body)", s.remote, name, h.token, len(body))
	if h.service == gameVersionHash && h.method == gameSetGameVersion {
		maybePushGameUpdate(s)
	}
	return true
}

// setGameVersionString reads SetGameVersionRequest {1 string}
// (docs/d2r-protocol.md: "SetGameVersionRequest {1 string}").
func setGameVersionString(body []byte) string {
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
