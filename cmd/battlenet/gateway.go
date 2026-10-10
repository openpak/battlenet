// Native ConnectionService for the Battle.net gateway (us.actual.battle.net
// moved here by the redirect): a small state machine reproducing the exchange
// captured from the real gateway (research-2026-09-13.md), with identity
// resolved through the account core on the authenticated Connect.
//
// Frames are GENERATED, not replayed: replaying a captured session verbatim
// got the anonymous Connect answered but the title tore the session down the
// moment the KeepAlive config arrived — the captured server_time and session
// id were an hour stale, and the client rejects server-initiated frames on a
// session it can see is dead. Each connection now gets a fresh identity in
// the captured shapes:
//
//	session id     XXXXXXXX6A8CD149-00000000000Cxxxx (33 chars)
//	server clock   microseconds since the unix epoch (captured ±2 s)
//	region/depot   constant descriptor, verbatim from the capture
//
// and the schedule follows the real gateway:
//
//	t=0        ConnectResponse (response to the anonymous Connect)
//	t=+30 s    KeepAlive config (method 3) — the title answers within
//	           100 ms with its KeepAlive and its authenticated Connect
//	t=auth     method 12, the 0xFE status-OK for the client's request,
//	           method 13, method 5 (the captured burst, in captured order)
//	t=auth+3 s logon_timeout (method 4)
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Captured constants (research-2026-09-13.md, 2026-09-14 relay sessions).
const (
	// KeepAlive config (method 3): identical bytes on every real session.
	keepaliveConfigHex = "000d08001003180128005d91694465"
	// The 0xFE status-OK answering the client's authenticated Connect
	// (request 3): constant.
	authResponseOKHex = "000908fe01180328003000"
	// logon_timeout (method 4), sent 3 s after the authenticated burst —
	// after which the real gateway closes the WebSocket (opcode 8).
	logonTimeoutHex = "000d08001004180528055d91694465088a041200"
	// Header field 11 is the target service hash (FNV-1a of its bnet name).
	// Every captured server push goes to AuthenticationClient.
	authClientHash     = uint32(0x71240e35)
	sessionServiceHash = uint32(0x1e688c05)
	sessionListener    = uint32(0x7fe36b32)
	authServerHash     = uint32(0x0decfc01)
	gameUtilitiesHash  = uint32(0x5dbb51c2)
	resourcesHash      = uint32(0xecbe75ba)
	accountServiceHash = uint32(0x62da0891)
	// The region/depot descriptor inside the ConnectResponse (field 8):
	// the SU/US region, content-address hash and prod.depot.battle.net URL.
	// Constant across every observed session.
	regionDescHex = "0a5c0d53550000157274656d1a20e9feaa6a8480f3990732dbb37bd543be5615d835e4dbb6f855866f7f5a1cee4b222e68747470733a2f2f70726f642e6465706f742e626174746c652e6e65742f247b686173687d2e247b75736167657d"
	// The gateway instance id baked into observed session ids (the low u32
	// of field 1: 6a8cd146/47/49 across two days). One gateway, one value.
	gatewayInstance = uint32(0x6a8cd149)
	// EntityId is two required fixed64 fields. The high word encodes kind,
	// region and program: account kind=1 is global; game-account kind=2 is
	// in region 1 (US) for program OSI (0x4f5349).
	battlenetAccountHigh = uint64(0x0100000000000000)
	d2rGameAccountHigh   = uint64(0x02000001004f5349)
)

// The observed schedule. The real gateway's KeepAlive config landed
// CR+30.19 s (CR = ConnectResponse receipt). But the decisive discovery is
// the CR's own arrival: the client answered every CR written ~10 ms after
// its connect by dropping it — it is not ready to read a response that
// early under emulation — and then aborted on the first server-initiated
// frame (a request while still awaiting the connect response is fatal).
// The relay accidentally passed because dialing upstream delayed the CR to
// +0.58 s. Replayed here: CR at +0.6 s (the client's Connect RPC timeout
// is 1.7 s — comfortable), KeepAlive config at CR+30.25 s.
const (
	connectResponseDelay = 600 * time.Millisecond
	keepAliveDelay       = 30250 * time.Millisecond
	logonTimeoutDelay    = 3 * time.Second
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("gateway: bad captured frame hex: " + err.Error())
	}
	return b
}

// Session stages.
const (
	stageAnonymous     = iota // connected; anonymous Connect answered
	stageArmed                // KeepAlive config sent; waiting for the authenticated Connect
	stageAuthenticated        // authenticated Connect answered; capturing past logon
)

// sessionIDCounter seeds the C component near the last value observed on the
// real gateway (0xc37f7 on 2026-09-14); each session increments it.
var sessionIDCounter atomic.Uint64

// gatewaySession is one title connection's progress through the flow, carrying
// the per-session identity every generated frame needs. The out channel
// serializes every write to the connection — replies, scheduled frames and
// timers all push onto it, and one writer goroutine drains it.
type gatewaySession struct {
	stage   int
	subject string
	remote  string
	sid     string
	a       uint32 // session id high u32 (random per connection)
	c       uint64 // session id counter component (this connection's value)
	out     chan []byte
	done    chan struct{} // closed when the connection is over; timers watch it

	// gameVersion is the D2R-services session's SetGameVersion string, used to
	// list only games of the same version.
	gameVersion string
}

func newGatewaySession(remote string) *gatewaySession {
	var raw [4]byte
	if _, err := crand.Read(raw[:]); err != nil {
		// Entropy failure on a modern kernel is a process-wide problem.
		panic("gateway: session entropy: " + err.Error())
	}
	a := binary.BigEndian.Uint32(raw[:])
	c := sessionIDCounter.Add(1)
	s := &gatewaySession{
		remote: remote,
		a:      a,
		c:      c,
		sid:    fmt.Sprintf("%08X%08X-%016X", a, gatewayInstance, c),
		out:    make(chan []byte, 16),
		done:   make(chan struct{}),
	}
	log.Printf("GATEWAY %s: session %s", remote, s.sid)
	return s
}

// send queues one frame for the writer goroutine. It abandons the frame if
// the connection is already over rather than leak a timer's write.
func (s *gatewaySession) send(frame []byte) {
	select {
	case <-s.done:
	case s.out <- frame:
	}
}

// arm schedules fn after d, skipped if the session ends first.
func (s *gatewaySession) arm(d time.Duration, fn func()) {
	time.AfterFunc(d, func() {
		select {
		case <-s.done:
		default:
			fn()
		}
	})
}

// pbLen encodes a varint length/value. main.go's pbVarint already encodes
// key+value for wire type 0; this is the bare value encoding.
func pbLen(n uint64) []byte {
	out := make([]byte, 0, 10)
	for n >= 0x80 {
		out = append(out, byte(n)|0x80)
		n >>= 7
	}
	return append(out, byte(n))
}

// pbBytes encodes a length-delimited protobuf field.
func pbBytes(field uint64, b []byte) []byte {
	out := append([]byte{byte(field<<3 | 2)}, pbLen(uint64(len(b)))...)
	return append(out, b...)
}

// pbFixed32 encodes a fixed32 protobuf field: key byte, then 4 raw bytes.
func pbFixed32(field uint64, b []byte) []byte {
	return append([]byte{byte(field<<3 | 5)}, b...)
}

// pbFixed64Value encodes a protobuf fixed64 field. EntityId uses fixed64,
// not varint; using pbVarint here produces a different wire type and the
// client cannot parse the otherwise plausible LogonResult.
func pbFixed64Value(field, value uint64) []byte {
	out := make([]byte, 9)
	out[0] = byte(field<<3 | 1)
	binary.LittleEndian.PutUint64(out[1:], value)
	return out
}

func entityID(high, low uint64) []byte {
	out := pbFixed64Value(1, high)
	return append(out, pbFixed64Value(2, low)...)
}

// pbFixed32U32 encodes a fixed32 field from a uint32 (little-endian).
// Program fourccs ride fixed32: 'OSI' is 0x004F5349.
func pbFixed32U32(field uint64, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return pbFixed32(field, b[:])
}

// d2rProgram is the fixed32 program tag the D2R client uses ('OSI').
const d2rProgram = uint32(0x004F5349)

// buildAccountState answers AccountService.GetAccountState with a populated
// GetAccountStateResponse {1: AccountState, 2: AccountFieldTags}, laid out
// per bgs.protocol.account.v1 (descriptor pool embedded in the title —
// account_service.proto + account_types.proto). An empty body parses but
// carries no account-level session (the client logs "Failed to retrieve
// account level session due to reason 4" and the D2R layer never starts), so
// every identity the D2R layer keys on is filled: battle tag + country +
// region at account level, the OSI game level, and the game-account handle
// list the session is retrieved from.
func buildAccountState(subject string) ([]byte, error) {
	accountID, err := strconv.ParseUint(subject, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("battlenet subject %q is not numeric: %w", subject, err)
	}

	level := pbBytes(5, []byte("US"))                            // country
	level = append(level, pbVarint(6, 1)...)                     // preferred_region: US
	level = append(level, pbBytes(8, []byte("OpenPak#0001"))...) // battle_tag

	gameLevel := pbBytes(8, []byte("OSI"))
	gameLevel = append(gameLevel, pbFixed32U32(9, d2rProgram)...)

	handle := pbFixed32U32(1, uint32(accountID))
	handle = append(handle, pbFixed32U32(2, d2rProgram)...)
	handle = append(handle, pbVarint(3, 1)...) // region: US
	accounts := pbVarint(3, 1)
	accounts = append(accounts, pbBytes(4, handle)...)

	state := pbBytes(1, level)
	state = append(state, pbBytes(5, gameLevel)...)
	state = append(state, pbBytes(7, accounts)...)

	programTag := pbFixed32U32(1, d2rProgram)
	programTag = append(programTag, pbFixed32U32(2, 1)...)
	regionTag := pbVarint(1, 1)
	regionTag = append(regionTag, pbFixed32U32(2, 1)...)
	tags := pbFixed32U32(2, 1)                     // account_level_info_tag
	tags = append(tags, pbFixed32U32(3, 1)...)     // privacy_info_tag
	tags = append(tags, pbFixed32U32(4, 1)...)     // parental_control_info_tag
	tags = append(tags, pbBytes(7, programTag)...) // game_level_info_tags
	tags = append(tags, pbBytes(9, programTag)...) // game_status_tags
	tags = append(tags, pbBytes(11, regionTag)...) // game_account_tags
	tags = append(tags, pbFixed32U32(12, 1)...)    // security_status_tag

	out := pbBytes(1, state)
	return append(out, pbBytes(2, tags)...), nil
}

// buildSuccessfulLogonResult constructs the public
// bgs.protocol.authentication.v1.LogonResult shape used by successful BGS
// servers. The session key is fresh per authentication and deliberately has
// the observed 64-byte length.
func buildSuccessfulLogonResult(subject string) ([]byte, error) {
	accountID, err := strconv.ParseUint(subject, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("battlenet subject %q is not numeric: %w", subject, err)
	}

	sessionKey := make([]byte, 64)
	if _, err := crand.Read(sessionKey); err != nil {
		return nil, fmt.Errorf("session key entropy: %w", err)
	}

	result := pbVarint(1, 0) // error_code: ERROR_OK
	result = append(result, pbBytes(2, entityID(battlenetAccountHigh, accountID))...)
	result = append(result, pbBytes(3, entityID(d2rGameAccountHigh, accountID))...)
	result = append(result, pbVarint(5, 1)...) // available_region: US
	result = append(result, pbVarint(6, 1)...) // connected_region: US
	result = append(result, pbBytes(7, []byte("OpenPak#0001"))...)
	result = append(result, pbBytes(8, []byte("US"))...)
	result = append(result, pbBytes(9, sessionKey)...)
	return result, nil
}

type bgsKey struct {
	hash   uint32
	method uint64
}

type bgsMethod struct {
	name   string
	oneway bool
}

func bgsName(hash uint32, method uint64) string {
	if m, ok := bgsMethods[bgsKey{hash, method}]; ok {
		return m.name
	}
	return fmt.Sprintf("unknown(%#08x).%d", hash, method)
}

// buildConnectResponse answers the anonymous ConnectionService.Connect
// (request 0) with the fresh session identity. Captured shape:
// {1: {A: random u32, B: gateway instance},
//
//	2: {C: connection counter, D: unix seconds},
//	6: server clock µs, 8: region descriptor, 9: session id, 10: 1}.
func (s *gatewaySession) buildConnectResponse() []byte {
	// Response header: service 0xFE, echoed request id, timeout 168, status OK.
	header := pbVarint(1, 0xFE)
	header = append(header, pbVarint(3, 0)...)
	header = append(header, pbVarint(5, 168)...)
	header = append(header, pbVarint(6, 0)...)

	inner1 := append(pbVarint(1, uint64(s.a)), pbVarint(2, uint64(gatewayInstance))...)
	inner2 := append(pbVarint(1, s.c), pbVarint(2, uint64(now().Unix()))...)
	var msg []byte
	msg = append(msg, pbBytes(1, inner1)...)
	msg = append(msg, pbBytes(2, inner2)...)
	msg = append(msg, pbVarint(6, uint64(now().UnixMicro()))...)
	msg = append(msg, pbBytes(8, mustHex(regionDescHex))...)
	msg = append(msg, pbBytes(9, []byte(s.sid))...)
	msg = append(msg, pbVarint(10, 1)...)

	out := make([]byte, 2, 2+len(header)+len(msg))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, msg...)
}

// buildServerRPC builds a server-initiated AuthenticationClient frame:
// header {service 0, method, server request id, payload size, service hash
// at field 11, field 13 as captured} plus message. Shapes captured: method 12 carries
// {1: 1, 2: server clock µs, 3: 0}; method 13 an empty message; method 5 the
// logon timeout value {1: 522}.
func (s *gatewaySession) buildServerRPC(method, requestID uint64, msg []byte) []byte {
	return s.buildPush(authClientHash, method, requestID, msg)
}

// buildPush addresses a server-initiated call to the listener with the given
// service hash. Header field 5 is the payload size (the captured "timeouts"
// 13/0/3 were exactly the payload lengths); with 5 = 0 the body is read as
// empty. Field 13 carries the connection id as captured.
func (s *gatewaySession) buildPush(hash uint32, method, requestID uint64, msg []byte) []byte {
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], hash)
	header := pbVarint(1, 0)
	header = append(header, pbVarint(2, method)...)
	header = append(header, pbVarint(3, requestID)...)
	header = append(header, pbVarint(5, uint64(len(msg)))...)
	header = append(header, pbFixed32(11, h[:])...)
	header = append(header, pbBytes(13, []byte(s.sid))...)
	out := make([]byte, 2, 2+len(header)+len(msg))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, msg...)
}

// buildRPCStatus answers a request with an error status and no body.
func buildRPCStatus(requestID, status uint64) []byte {
	header := pbVarint(1, 0xFE)
	header = append(header, pbVarint(3, requestID)...)
	header = append(header, pbVarint(6, status)...)
	out := make([]byte, 2, 2+len(header))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	return append(out, header...)
}

// buildRPCResponse returns a successful response to a client request. The
// response service is 0xFE, the request id is echoed, field 5 carries the
// body size (the captured ConnectResponse: 5 = 168 over a 168-byte body;
// without it the body is read as empty), and the body follows the header.
func buildRPCResponse(requestID uint64, msg []byte) []byte {
	header := pbVarint(1, 0xFE)
	header = append(header, pbVarint(3, requestID)...)
	header = append(header, pbVarint(5, uint64(len(msg)))...)
	header = append(header, pbVarint(6, 0)...)
	out := make([]byte, 2, 2+len(header)+len(msg))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, msg...)
}

// Experiment discriminator: replay the captured ConnectResponse verbatim
// (real A/C/D/µs/session id — including its 67-minute D-vs-µs gap) instead
// of generating fresh values. Selected by BATTLENET_CR_MODE=replay.
var replayCR = mustHex(connectResponseReplayHex)

// Captured verbatim, 2026-09-14 11:47 relayed session (DC4EAA6F6A8CD149).
const connectResponseReplayHex = "000a08fe01180028a80130000a0c08efd4bae20d10c9a2b3d406120a0883ef3010c5bd9fd5063089bfe4f7ffed9603425e0a5c0d53550000157274656d1a20e9feaa6a8480f3990732dbb37bd543be5615d835e4dbb6f855866f7f5a1cee4b222e68747470733a2f2f70726f642e6465706f742e626174746c652e6e65742f247b686173687d2e247b75736167657d4a213946363441324636413843443134372d303030303030303030303043333231395001"

func (s *gatewaySession) connectResponse() []byte {
	if env("BATTLENET_CR_MODE", "") == "replay" {
		return replayCR
	}
	return s.buildConnectResponse()
}

// handleNativeRPC answers one binary frame according to the session's stage.
// The return decides whether the connection stays open.
func handleNativeRPC(s *gatewaySession, payload []byte) bool {
	h := parseRPCHeader(payload)
	log.Printf("GATEWAY %s rpc service=%d method=%d request=%d (%d bytes)",
		s.remote, h.serviceID, h.methodID, h.requestID, len(payload))

	switch s.stage {
	case stageAnonymous:
		// Anonymous Connect: answer with the fresh ConnectResponse alone,
		// and arm the KeepAlive config for +30 s — when the real gateway
		// sends it. Sending it early (t=0) gets it dropped on the floor.
		if h.serviceID == 0 && h.methodID == 1 {
			s.stage = stageArmed
			// The client drops a response that arrives immediately after its
			// request — see the schedule comment. Wait, then send.
			s.arm(connectResponseDelay, func() {
				s.send(s.connectResponse())
				log.Printf("GATEWAY %s: anonymous connect answered at +%v; KeepAlive config armed for CR+%v",
					s.remote, connectResponseDelay, keepAliveDelay)
				s.arm(keepAliveDelay, func() {
					log.Printf("GATEWAY %s schedule: KeepAlive config (method 3) at CR+%v", s.remote, keepAliveDelay)
					s.send(mustHex(keepaliveConfigHex))
				})
			})
			return true
		}
		return true

	case stageArmed:
		// The authenticated Connect carries the JWTs in its payload, and in
		// the captures arrived within a second of the KeepAlive config.
		if h.serviceID == 0 && h.methodID == 1 && strings.Contains(string(payload), "eyJ") {
			idToken := extractFirstJWT(payload)
			subject, err := identities.resolve(context.Background(), idToken)
			if err != nil {
				// The failure shape for an authenticated Connect is not
				// captured; closing is the one answer that cannot be wrong.
				log.Printf("GATEWAY %s: identity rejected: %v", s.remote, err)
				return false
			}
			s.subject = subject
			go recordLoginVersion(s.remote, subject, idToken)
			// The captured burst, in captured order: the method-12
			// ConnectResponse, the status-OK for the client's request,
			// then methods 13 and 5. Server request ids 2/3/4 are the
			// counter values the capture shows.
			// BATTLENET_AUTH_CR_STANDARD: the client's authenticated Connect
			// (service 0, method 1, request id 1) has never received a proper
			// response — the anonymous one got a {service 0xFE, status OK}
			// frame + payload, the authenticated one only the method-12
			// extension burst replayed from Blizzard's rejection capture.
			// This answers it the same way the anonymous one was answered:
			// 0xFE header echoing request id 1, status 0, plus the real
			// bnet.protocol ConnectResponse {1: server_id, 3: bind_result=0,
			// 6: server_time, 7: use_bindless_rpc=true}.
			if env("BATTLENET_AUTH_CR_STANDARD", "") != "" {
				hdr := append(pbVarint(1, 0xFE), pbVarint(3, 1)...)
				hdr = append(hdr, pbVarint(5, 168)...)
				hdr = append(hdr, pbVarint(6, 0)...)
				payload := pbVarint(1, 1)
				payload = append(payload, pbVarint(3, 0)...)
				payload = append(payload, pbVarint(6, uint64(now().Unix()))...)
				payload = append(payload, pbVarint(7, 1)...)
				frame := make([]byte, 2, 2+len(hdr)+len(payload))
				binary.BigEndian.PutUint16(frame, uint16(len(hdr)))
				frame = append(frame, hdr...)
				frame = append(frame, payload...)
				s.send(frame)
				log.Printf("GATEWAY %s: standard auth ConnectResponse sent (0xFE/req1)", s.remote)
			}
			// Method 12's captured payload {1: 1, 2: µs, 3: 0} came from the
			// rejection session; if field 1 is a result code the capture is
			// refusing logon on our behalf. BATTLENET_METHOD12_RESULT
			// overrides field 1's value only ("skip" drops the frame; unset
			// keeps the captured 1). Fields 2/3 stay generated.
			method12 := pbVarint(3, 0)
			if r := env("BATTLENET_METHOD12_RESULT", "1"); strings.EqualFold(r, "skip") {
				method12 = nil
			} else if v, err := strconv.ParseUint(r, 0, 64); err == nil {
				method12 = append(pbVarint(1, v), method12...)
			} else {
				method12 = append(pbVarint(1, 1), method12...)
			}
			if len(method12) > 0 {
				method12 = append(pbVarint(2, uint64(now().UnixMicro())), method12...)
				s.send(s.buildServerRPC(12, 2, method12))
			}
			// The captured bare status-OK (0xFE/req-3). BATTLENET_AUTH_ACK_REQ
			// re-addresses it — in our sessions the client's auth Connect is
			// request 1, so the captured req-3 ack matches nothing;
			// BATTLENET_AUTH_ACK=skip drops the frame.
			if ack := env("BATTLENET_AUTH_ACK", ""); !strings.EqualFold(ack, "skip") {
				reqID, _ := strconv.ParseUint(env("BATTLENET_AUTH_ACK_REQ", "3"), 0, 64)
				hdr := append(pbVarint(1, 0xFE), pbVarint(3, reqID)...)
				hdr = append(hdr, pbVarint(6, 0)...)
				frame := make([]byte, 2, 2+len(hdr))
				binary.BigEndian.PutUint16(frame, uint16(len(hdr)))
				frame = append(frame, hdr...)
				s.send(frame)
			}
			// BATTLENET_METHOD13=skip drops the push; any other value is the
			// raw payload hex (the captured frame carried an empty message).
			if p := env("BATTLENET_METHOD13", ""); !strings.EqualFold(p, "skip") {
				var payload []byte
				if p != "" {
					payload = mustHex(p)
				}
				s.send(s.buildServerRPC(13, 3, payload))
			}
			// BATTLENET_LOGON_RESULT: hex payload pushed as OnLogonComplete.
			// BATTLENET_LOGON_METHODS lists candidate method numbers (the
			// D2R dialect's auth-listener numbering is only pinned at 12=
			// OnLogonQueueUpdate and 13=OnLogonQueueEnd); unknown methods are
			// dropped silently by the client, so fanning out is safe.
			if lr := env("BATTLENET_LOGON_RESULT", ""); lr != "" {
				for _, m := range strings.Split(env("BATTLENET_LOGON_METHODS", "4"), ",") {
					if n, err := strconv.ParseUint(strings.TrimSpace(m), 0, 64); err == nil {
						s.send(s.buildServerRPC(n, 5, mustHex(lr)))
					}
				}
				log.Printf("GATEWAY %s: LogonResult pushed", s.remote)
			}
			// BATTLENET_QUEUE_END: a second OnLogonQueueUpdate with position
			// 0 one second later — the way the queue UI actually clears
			// (position 0), observed in E7.
			if env("BATTLENET_QUEUE_END", "") != "" {
				s.arm(time.Second, func() {
					p := append(pbVarint(1, 0), pbVarint(2, uint64(now().UnixMicro()))...)
					p = append(p, pbVarint(3, 0)...)
					s.send(s.buildServerRPC(12, 6, p))
					log.Printf("GATEWAY %s: queue-end (position 0) pushed", s.remote)
				})
			}
			method5Payload := env("BATTLENET_METHOD5_HEX", "088a04")
			if env("BATTLENET_LOGON_SUCCESS", "") != "" {
				result, err := buildSuccessfulLogonResult(subject)
				if err != nil {
					log.Printf("GATEWAY %s: successful LogonResult: %v", s.remote, err)
					return false
				}
				s.send(s.buildServerRPC(5, 4, result))
				log.Printf("GATEWAY %s: successful LogonResult pushed on method 5", s.remote)
			} else if !strings.EqualFold(method5Payload, "skip") {
				s.send(s.buildServerRPC(5, 4, mustHex(method5Payload)))
			}
			s.stage = stageAuthenticated
			// BATTLENET_GATEWAY_HOLD: skip the logon_timeout+close. The
			// auth+3s close came from the RELAYED capture, where Blizzard had
			// already decided to reject the token — it is rejection behavior,
			// not the healthy schedule. With the close skipped the session
			// stays open (the logon_timeout value says 522 s), and whatever
			// the client does next is visible instead of preempted.
			if env("BATTLENET_GATEWAY_HOLD", "") != "" {
				log.Printf("GATEWAY %s: hold mode — session stays open for the client to log on", s.remote)
				return true
			}
			s.arm(logonTimeoutDelay, func() {
				log.Printf("GATEWAY %s schedule: logon_timeout (method 4) at auth+%v", s.remote, logonTimeoutDelay)
				s.send(mustHex(logonTimeoutHex))
				// The real gateway closes the WebSocket in the same breath as
				// the logon_timeout (77 µs apart in the capture, status 1000).
				// The title waits for this close to finalize the stage.
				s.arm(100*time.Millisecond, func() {
					s.send([]byte{0x88, 0x02, 0x03, 0xE8})
					log.Printf("GATEWAY %s: close sent (relay-matched flow complete)", s.remote)
				})
			})
			log.Printf("GATEWAY %s: authenticated connect answered for battlenet subject %s", s.remote, subject)
			return true
		}
		// KeepAlive (method 5) is NO_RESPONSE in bnet.protocol and the
		// capture shows the real gateway answering nothing; log the rest.
		return true

	default:
		// Authenticated: from here the title speaks logon and game services.
		// Capture whole — this is the logon evidence the next stage needs —
		// and leave unanswered, since the correct response shape is exactly
		// what the capture is for.
		log.Printf("GATEWAY %s authenticated rpc: service=%d method=%d request=%d payload=%x",
			s.remote, h.serviceID, h.methodID, h.requestID, payload)
		// BATTLENET_METHOD7_ECHO: the title's method-7 request (payload
		// {1: 0}) lands right after the authenticated burst and sits
		// unanswered — an outstanding RPC the SDK may block on. Echo a
		// 0xFE status-OK response frame carrying the request id, the same
		// shape the title itself uses to answer server requests.
		if env("BATTLENET_METHOD7_ECHO", "") != "" && h.methodID == 7 {
			s.send(buildRPCResponse(h.requestID, nil))
			log.Printf("GATEWAY %s: method-7 answered (0xFE/req%d status-OK)", s.remote, h.requestID)
		}
		if env("BATTLENET_LOGON_SUCCESS", "") != "" {
			return s.answerLoggedOn(h)
		}
		return true
	}
}

// answerLoggedOn serves the title once the logon completed: every request
// is named from the descriptor table and answered, so the client never
// stalls on a silent RPC. Requests with a known response shape get it;
// the rest get an empty status-OK (all response fields are optional).
func (s *gatewaySession) answerLoggedOn(h rpcHeader) bool {
	if h.serviceID == 0xFE { // the client answering one of our pushes
		return true
	}
	key := bgsKey{h.serviceHash, h.methodID}
	name := bgsName(h.serviceHash, h.methodID)
	if bgsMethods[key].oneway {
		log.Printf("GATEWAY %s: %s (oneway, req%d)", s.remote, name, h.requestID)
		return true
	}
	var resp, push []byte
	switch key {
	case bgsKey{authServerHash, 8}: // GenerateWebCredentials {1: web_credentials}
		credential := make([]byte, 32)
		if _, err := crand.Read(credential); err != nil {
			log.Printf("GATEWAY %s: web credential entropy: %v", s.remote, err)
			return false
		}
		resp = pbBytes(1, credential)
	case bgsKey{sessionServiceHash, 1}: // CreateSession
		sessionID := make([]byte, 16)
		if _, err := crand.Read(sessionID); err != nil {
			log.Printf("GATEWAY %s: session id entropy: %v", s.remote, err)
			return false
		}
		id := hex.EncodeToString(sessionID)
		resp = pbBytes(1, []byte(id)) // CreateSessionResponse.session_id
		push = s.buildPush(sessionListener, 1, 5, sessionCreated(h.body, id))
	case bgsKey{gameUtilitiesHash, 1}: // ProcessTask: the D2R login rides here
		if !bytes.Contains(h.body, []byte("d2r_connection.LoginRequest")) {
			break
		}
		resp = d2rLoginResponse(s.subject)
		// Probe: a late answer tells which 15 s state times out — the
		// teardown stays at +15 s if Login rejects it, moves out if Login
		// takes it and Connect is the one stuck.
		if d, err := time.ParseDuration(env("BATTLENET_PROCESSTASK_DELAY", "")); err == nil {
			frame := buildRPCResponse(h.requestID, resp)
			s.arm(d, func() {
				s.send(frame)
				log.Printf("GATEWAY %s: %s answered after %s (req%d)", s.remote, name, d, h.requestID)
			})
			return true
		}
	case bgsKey{resourcesHash, 1}:
		// GetContentHandle's ContentHandle has required fields, so an empty
		// body cannot parse; refuse cleanly instead (ERROR_NOT_EXISTS).
		s.send(buildRPCStatus(h.requestID, 4))
		log.Printf("GATEWAY %s: %s refused (req%d, status 4)", s.remote, name, h.requestID)
		return true
	case bgsKey{accountServiceHash, 30}: // GetAccountState
		// The D2R layer retrieves its account-level session here; an empty
		// body parses but leaves it unset ("Failed to retrieve account
		// level session due to reason 4", and the game-server stage never
		// starts), so the state is populated from the subject.
		state, err := buildAccountState(s.subject)
		if err != nil {
			log.Printf("GATEWAY %s: account state: %v", s.remote, err)
			return false
		}
		resp = state
	}
	s.send(buildRPCResponse(h.requestID, resp))
	log.Printf("GATEWAY %s: %s answered (req%d, %d-byte body)", s.remote, name, h.requestID, len(resp))
	if push != nil {
		s.send(push)
		log.Printf("GATEWAY %s: SessionListener.OnSessionCreated pushed", s.remote)
	}
	return true
}

// loginResponse encodes classic.protocol.v1.d2r_connection.LoginResponse.
// The title keeps only field 1, the D2R-services WebSocket base (it appends
// "/OSI/v2/rpc/client"; ws: is plain, anything else TLS), and field 2, the
// opaque token it sends back as AuthSessionRequest field 1. Both must be
// non-empty or the online flow never dials. Fields 3 (bool) and 4
// (GameUpdateRequest) are ignored by the client.
func loginResponse(subject string) []byte {
	login := pbBytes(1, []byte(env("BATTLENET_D2R_SERVICES_URL", "wss://us.actual.battle.net:1119")))
	return append(login, pbBytes(2, []byte(d2rSessionToken(subject)))...)
}

// The D2R-services WebSocket is a second connection that carries no identity
// of its own: the only link back to the authenticated bgs logon is the
// LoginResponse field-2 token the title echoes in AuthSession. Until v0.2.0
// that token was the bare "openpak:<subject>", so any client could claim any
// account's roster by writing a different number. It is now
// "openpak:<subject>.<mac>", the MAC an HMAC-SHA256 over the subject under a
// server key, and a token that does not verify gives the session no account.
// The title treats field 2 as opaque bytes (it only echoes it), so the longer
// value changes nothing on its side.
//
// The key is BATTLENET_SESSION_KEY when set, else derived from
// ACCOUNT_INTERNAL_KEY (the secret every production gateway already has, so a
// token outlives a restart), else random per process (identity stub / local
// runs: a restart then needs a fresh login).
var (
	sessionKeyOnce sync.Once
	sessionKey     []byte
)

func d2rSessionKey() []byte {
	sessionKeyOnce.Do(func() {
		if k := os.Getenv("BATTLENET_SESSION_KEY"); k != "" {
			sessionKey = []byte(k)
			return
		}
		if k := os.Getenv("ACCOUNT_INTERNAL_KEY"); k != "" {
			m := hmac.New(sha256.New, []byte(k))
			m.Write([]byte("openpak battlenet d2r session token v1"))
			sessionKey = m.Sum(nil)
			return
		}
		sessionKey = make([]byte, 32)
		if _, err := crand.Read(sessionKey); err != nil {
			panic("battlenet: session key entropy: " + err.Error())
		}
	})
	return sessionKey
}

func d2rSessionMAC(subject string) string {
	m := hmac.New(sha256.New, d2rSessionKey())
	m.Write([]byte(subject))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// d2rSessionToken is the LoginResponse field-2 value for a subject.
func d2rSessionToken(subject string) string {
	return "openpak:" + subject + "." + d2rSessionMAC(subject)
}

// d2rTokenSubject verifies a token from AuthSession and returns its subject,
// or "" when it is not one we issued.
func d2rTokenSubject(token string) string {
	rest, ok := strings.CutPrefix(token, "openpak:")
	if !ok {
		return ""
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		return ""
	}
	subject, mac := rest[:i], rest[i+1:]
	if !hmac.Equal([]byte(mac), []byte(d2rSessionMAC(subject))) {
		return ""
	}
	return subject
}

// d2rLoginResponse wraps the LoginResponse in the ProcessTaskResponse
// attributes that mirror the request's client_request/protobuf_request pair
// (bgs.protocol.v2.Attribute {1: name, 2: Variant {4: string_value}}).
func d2rLoginResponse(subject string) []byte {
	attr := func(name string, value []byte) []byte {
		return pbBytes(1, append(pbBytes(1, []byte(name)), pbBytes(2, value)...))
	}
	out := attr("client_response", pbBytes(4, []byte("classic.protocol.v1.d2r_connection.LoginResponse")))
	// Both values must be Variant.string_value (4): the title reads only
	// that member, so a blob_value arrives as "" and parses as an empty
	// LoginResponse. protobuf_response carries the raw bytes, not base64.
	return append(out, attr("protobuf_response", pbBytes(4, loginResponse(subject)))...)
}

// sessionCreated builds SessionCreatedNotification {1: identity, 2: reason 0,
// 3: session_id, 4: session_key, 5: client_id} from the CreateSessionRequest,
// whose identity (field 1) and session_key (field 7) are echoed back. The
// title keeps field 5, not 3, as its session id (handler 0xcf98f8 → 0x1ca438);
// it later rides as header client_id on ProcessTask and DestroySession.
func sessionCreated(req []byte, sessionID string) []byte {
	var identity, key []byte
	for b := req; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				break
			}
			switch num {
			case 1:
				identity = v
			case 7:
				key = v
			}
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			break
		}
		b = b[m:]
	}
	out := pbBytes(1, identity)
	out = append(out, pbVarint(2, 0)...)
	out = append(out, pbBytes(3, []byte(sessionID))...)
	out = append(out, pbBytes(4, key)...)
	return append(out, pbBytes(5, []byte(sessionID))...)
}

// extractFirstJWT pulls the first JWT-looking string out of a frame payload
// (the authenticated Connect embeds the id_token as a length-delimited field).
func extractFirstJWT(payload []byte) string {
	s := string(payload)
	i := strings.Index(s, "eyJ")
	if i < 0 {
		return ""
	}
	j := i
	for j < len(s) {
		ch := s[j]
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-'
		if !ok {
			break
		}
		j++
	}
	return s[i:j]
}

// init seeds the session counter once, before any connection arrives.
func init() {
	sessionIDCounter.Store(0xc3800)
}

// now is indirect so the byte-fidelity test can freeze the clock.
var now = time.Now
