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
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
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
	// Header field 11 of every server-initiated frame: a fixed32 protocol
	// constant, not a timestamp (identical across sessions weeks apart).
	headerEpochHex = "350e2471"
	// The region/depot descriptor inside the ConnectResponse (field 8):
	// the SU/US region, content-address hash and prod.depot.battle.net URL.
	// Constant across every observed session.
	regionDescHex = "0a5c0d53550000157274656d1a20e9feaa6a8480f3990732dbb37bd543be5615d835e4dbb6f855866f7f5a1cee4b222e68747470733a2f2f70726f642e6465706f742e626174746c652e6e65742f247b686173687d2e247b75736167657d"
	// The gateway instance id baked into observed session ids (the low u32
	// of field 1: 6a8cd146/47/49 across two days). One gateway, one value.
	gatewayInstance = uint32(0x6a8cd149)
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
	stageAnonymous = iota // connected; anonymous Connect answered
	stageArmed            // KeepAlive config sent; waiting for the authenticated Connect
	stageAuthenticated    // authenticated Connect answered; capturing past logon
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

// serverRequestID builds the shared header tail of server-initiated frames:
// the protocol-constant field 11 and, where observed, the session id at
// field 13.
func serverRequestID(s *gatewaySession, withSID bool) []byte {
	out := pbFixed32(11, mustHex(headerEpochHex))
	if withSID {
		out = append(out, pbBytes(13, []byte(s.sid))...)
	}
	return out
}

// buildConnectResponse answers the anonymous ConnectionService.Connect
// (request 0) with the fresh session identity. Captured shape:
// {1: {A: random u32, B: gateway instance},
//  2: {C: connection counter, D: unix seconds},
//  6: server clock µs, 8: region descriptor, 9: session id, 10: 1}.
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

// buildServerRPC builds a server-initiated ConnectionService frame: header
// {service 0, method, server request id, timeout, constant field 11, session
// id at field 13} plus message. Shapes captured: method 12 carries
// {1: 1, 2: server clock µs, 3: 0}; method 13 an empty message; method 5 the
// logon timeout value {1: 522}.
func (s *gatewaySession) buildServerRPC(method, requestID, timeout uint64, msg []byte) []byte {
	header := pbVarint(1, 0) // service 0 = ConnectionService
	header = append(header, pbVarint(2, method)...)
	header = append(header, pbVarint(3, requestID)...)
	header = append(header, pbVarint(5, timeout)...)
	header = append(header, serverRequestID(s, true)...)
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
				s.send(s.buildServerRPC(12, 2, 13, method12))
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
				s.send(s.buildServerRPC(13, 3, 0, payload))
			}
			// Method 5's captured payload {1: 522} came from the rejection
			// session — 522 is the same value the title reports as "Cannot
			// Connect". If the client reads it as a status, the replayed
			// burst is refusing logon on our behalf. BATTLENET_METHOD5_HEX
			// overrides the payload; the value "skip" drops the frame.
			method5Payload := env("BATTLENET_METHOD5_HEX", "088a04")
			if !strings.EqualFold(method5Payload, "skip") {
				s.send(s.buildServerRPC(5, 4, 3, mustHex(method5Payload)))
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
			hdr := append(pbVarint(1, 0xFE), pbVarint(3, uint64(h.requestID))...)
			hdr = append(hdr, pbVarint(6, 0)...)
			frame := make([]byte, 2, 2+len(hdr))
			binary.BigEndian.PutUint16(frame, uint16(len(hdr)))
			frame = append(frame, hdr...)
			s.send(frame)
			log.Printf("GATEWAY %s: method-7 answered (0xFE/req%d status-OK)", s.remote, h.requestID)
		}
		return true
	}
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
