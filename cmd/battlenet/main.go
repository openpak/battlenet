// Command battlenet is OpenPak's Battle.net web tier: the shared login and
// routing surface every Blizzard title on a redirected console reaches before
// it reaches anything title-specific — geo.battle.net for region routing,
// telemetry-in.battle.net for telemetry, account.battle.net for login.
//
// What it serves today is a capture harness. Nobody has seen the requests a
// retail Switch title makes once the TLS handshake completes, because the
// handshake has never completed: Traefik's default cert fails the guest's
// PeerCa+HostName verification and no HTTP byte is ever spoken (see
// docs/research-2026-09-13.md). This front ends that: every request is logged
// whole — method, path, headers, body — because the bodies are the evidence,
// and each hostname gets the most defensible answer available rather than a
// 404. A wrong answer moves the failure somewhere visible, which is the point:
// the next capture names the shape actually wanted.
//
// It is the network's tier, not a title's: when a second Blizzard title
// arrives, this is the process it talks to first, and the per-title part hangs
// off the geo answer like the game servers hang off Impostor's matchmaker.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// upstream is the real login endpoint the relay forwards to.
var upstream = "https://account.battle.net"

var client = &http.Client{Timeout: 30 * time.Second}

// relay forwards the request to the real account endpoint and copies the
// answer back. Hop-by-hop headers are dropped in both directions; everything
// else, including the body, travels verbatim. Both sides are logged.
func relay(w http.ResponseWriter, r *http.Request, body []byte) {
	// The body arrives from dump, which logs and rewinds it.
	req, err := http.NewRequest(r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		log.Printf("RELAY build error: %v", err)
		http.Error(w, "relay", http.StatusBadGateway)
		return
	}

	for k, v := range r.Header {
		switch k {
		case "X-Forwarded-For", "X-Forwarded-Port", "X-Forwarded-Proto", "X-Forwarded-Server", "X-Forwarded-Host", "X-Real-Ip":
			continue
		}
		req.Header[k] = v
	}
	req.Host = "account.battle.net"

	log.Printf("RELAY -> %s %s (%d bytes)", r.Method, r.URL.RequestURI(), len(body))

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("RELAY upstream error: %v", err)
		http.Error(w, "relay", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	log.Printf("RELAY <- %d (%d bytes) for %s", resp.StatusCode, len(respBody), r.URL.RequestURI())
	if len(respBody) > 0 {
		log.Printf("RELAY body: %s", respBody)
	}

	for k, v := range resp.Header {
		switch k {
		case "Content-Length", "Transfer-Encoding", "Content-Encoding":
			continue
		}
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// dump logs a request whole. Bodies are capped because a telemetry sink can be
// asked to carry kilobytes and none of it is worth a wedged journal. The body is
// put back on the request afterwards: the relay reads it a second time.
func dump(w http.ResponseWriter, r *http.Request, what string) []byte {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	log.Printf("%s %s %s from %s", what, r.Method, r.URL.String(), r.Header.Get("User-Agent"))
	for k, v := range r.Header {
		log.Printf("  %s: %s", k, strings.Join(v, ", "))
	}
	if len(body) > 0 && what == "CHECK_TOKEN" {
		log.Printf("  body: [redacted authentication payload, %d bytes]", len(body))
	} else if len(body) > 0 {
		log.Printf("  body (%d bytes): %s", len(body), body)
	}
	return body
}

func main() {
	// Sub-second timestamps: the gateway schedule races the title's connect
	// timeout, and seconds-only logs made the two indistinguishable.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// The D2R roster outlives the process: a deploy used to cost the player
	// their character.
	loadCharacters()

	addr := env("BATTLENET_HTTP_ADDR", ":21500")

	// BATTLENET_IDENTITY_STUB: run the gateway without the account core (the
	// local experiment loop). Any authenticated Connect resolves to the stub
	// subject; nothing else changes.
	if subject := env("BATTLENET_IDENTITY_STUB", ""); subject != "" {
		identities = stubIdentity{subject: subject}
		log.Printf("identity stub active (subject %s)", subject)
	} else {
		ident, err := newIdentityClient(context.Background(), env("ACCOUNT_GRPC_ADDR", "openpak-account:20001"), os.Getenv("ACCOUNT_INTERNAL_KEY"))
		if err != nil {
			log.Fatalf("identity client: %v", err)
		}
		defer ident.Close()
		identities = ident
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// geo.battle.net is the region router: the first connection a client makes,
	// asking which endpoints to use for everything after. The real response
	// shape is unrecorded; what is logged here is the question, which tells us
	// which service entry the client reads. The answer below carries the same
	// three hostnames back — the client is already talking to them — plus the
	// gateway name PC clients use, so the next capture names stage four either
	// way. country_alpha2: the D2 layer's BGSEnvironmentMap keys on it and
	// logs "Unknown country code" when it is missing (the binary's parser
	// strings name the field; the 2026-09-14 evening runs showed the empty
	// lookup against the old answer).
	geo := func(w http.ResponseWriter, r *http.Request) {
		dump(w, r, "GEO")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
  "datacenter_region": "US",
  "country_alpha2": "US",
  "loc": "US",
  "svc": {
    "account": {"name": "account.battle.net"},
    "telemetry": {"name": "telemetry-in.battle.net"},
    "gateway": {"name": "us.actual.battle.net", "port": 1119}
  }
}`))
	}

	mux.HandleFunc("/", geo)

	// telemetry-in.battle.net ingests and keeps nothing the client waits on.
	// The Fenway client logs "FlowControl: HTTP response header doesn't have
	// an eTag" against bare answers, so every response carries a stable ETag
	// derived from the path, and a matching If-None-Match is honoured with
	// 304 — the client asked to validate, so let it.
	telemetry := func(w http.ResponseWriter, r *http.Request) {
		dump(w, r, "TELEMETRY")
		etag := fmt.Sprintf(`"telemetry-%08x"`, sha1.Sum([]byte(r.URL.Path)))
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}

	// account.battle.net is the login. Every request is logged whole; answers
	// are 401 with a JSON body, because a wrong-but-well-formed denial teaches
	// the same lesson as silence without pretending the login worked.
	// account.battle.net is the login. Its first call is an unauthenticated
	// region lookup — the request carries no Authorization header at all — so
	// denying it there ends the web login before it starts. What the client
	// reads out of the answer is not yet known; the fields below are the common
	// spellings of the answer such a lookup exists to give, and if the client
	// wanted a different one, the next capture says where it stopped.
	account := func(w http.ResponseWriter, r *http.Request) {
		// /login/check_token is the Nintendo-BAAS bridge: the game offers the
		// id_token of its switch sub-account and OpenPak answers with the same
		// person's battlenet sub-account — both are adapter links on one core
		// account, so the exchange is two lookups and a first-login provisioning
		// instead of the linking ceremony real Blizzard needs. The response
		// shape is iterated against the game; "result" leads because that is
		// how the endpoint's own errors speak.
		if strings.Split(r.URL.Path, "?")[0] == "/login/check_token" {
			body := dump(w, r, "CHECK_TOKEN")

			var req struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(body, &req); err != nil || req.Token == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"result":"INVALID_REQUEST","error_message":"token missing"}`))
				return
			}

			subject, err := identities.resolve(r.Context(), req.Token)
			if err != nil {
				log.Printf("CHECK_TOKEN rejected: %v", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"result":"INVALID_TOKEN","error_message":"INVALID_AUTHENTICATION_TOKEN"}`))
				return
			}

			log.Printf("CHECK_TOKEN issued session for battlenet subject %s", subject)

			// The D2 layer reads "result", "country_alpha2", then
			// "client_response" — the LoginResponse TYPE NAME — and parses
			// base64 "protobuf_response" as that message, the same pair the
			// in-band ProcessTask answer carries.
			if tmpl := env("BATTLENET_CHECK_TOKEN_TEMPLATE", ""); tmpl != "" {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(strings.ReplaceAll(tmpl, "${UID}", subject)))
				return
			}
			login := loginResponse(subject)
			if override := env("BATTLENET_LOGIN_RESPONSE_HEX", ""); override != "" {
				login = mustHex(override)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"result":%s,"country_alpha2":"US","client_response":%q,"protobuf_response":%q,"uid":%q,"account_id":%q,"region":"US","program":"osi","platform":"swch"}`,
				env("BATTLENET_CHECK_TOKEN_RESULT", "0"), "classic.protocol.v1.d2r_connection.LoginResponse", base64.StdEncoding.EncodeToString(login), subject, subject)
			return
		}

		// Everything else on the login host goes to the real endpoint and is
		// recorded on the way through — the relay exists to answer questions
		// OpenPak does not have an implementation for yet, endpoint by endpoint,
		// and shrinks as they are answered.
		relay(w, r, dump(w, r, "ACCOUNT"))
		return
	}

	// One listener, host-routed: Traefik terminates TLS for the three names and
	// forwards here with the Host header intact, which is how the requests
	// find their handlers. The console entrypoint carries all of them.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok\n"))
			return
		}
		switch host := strings.Split(r.Host, ":")[0]; host {
		case "account.battle.net":
			account(w, r)
		case "telemetry-in.battle.net":
			telemetry(w, r)
		case "geo.battle.net":
			geo(w, r)
		default:
			log.Printf("UNKNOWN HOST %q %s %s", r.Host, r.Method, r.URL.Path)
			dump(w, r, "UNKNOWN-HOST")
			http.NotFound(w, r)
		}
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("battlenet web tier on %s (geo, telemetry-in, account)", addr)

	go gatewaySink(env("BATTLENET_GATEWAY_ADDR", ":21501"))
	go gameProbeSink(env("BATTLENET_GAME_PROBE_ADDR", ":21502"))

	log.Fatal(server.ListenAndServe())
}

// gameProbeSink captures the transport the title opens after GameJoin. The
// public Traefik listener terminates TLS for us.actual.battle.net and forwards
// the clear stream here. Keeping this separate from gatewaySink matters: the
// game connection is not an HTTP WebSocket upgrade, and treating it as one
// hid the first client packet until the upgrade reader timed out.
//
// This is deliberately an observation endpoint, not a pretend game server.
// It logs exactly what the title sends and writes nothing until the game
// transport's framing and handshake have been established from evidence.
func gameProbeSink(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("game probe sink on %s: %v", addr, err)
		return
	}
	log.Printf("game probe sink on %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("GAMEPROBE accept: %v", err)
			continue
		}
		go captureGameProbe(conn)
	}
}

func captureGameProbe(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("GAMEPROBE connect from %s", remote)
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			log.Printf("GAMEPROBE %s RX %d bytes: %x", remote, n, buf[:n])
		}
		if err != nil {
			log.Printf("GAMEPROBE %s closed: %v", remote, err)
			return
		}
	}
}

// identities is the account-core client the check_token exchange resolves
// through; set at startup, never nil afterwards.
var identities identityResolver

// identityResolver is what check_token and the gateway's authenticated
// Connect use to turn a BAAS id_token into a battlenet subject.
type identityResolver interface {
	resolve(ctx context.Context, idToken string) (string, error)
	Close()
}

// stubIdentity stands in for the account core when the gateway runs locally
// (BATTLENET_IDENTITY_STUB): every token resolves, nothing is verified. It is
// an experiment harness, never a deployment mode.
type stubIdentity struct{ subject string }

func (s stubIdentity) resolve(_ context.Context, _ string) (string, error) { return s.subject, nil }
func (s stubIdentity) Close()                                              {}

// gatewaySink accepts the title's gateway connections (us.actual.battle.net
// moved to TCP 1119 by the redirect). The gateway turned out to be a
// WebSocket speaking the "v1.rpc.battle.net" subprotocol — Blizzard's
// protobuf RPC — so the sink completes the upgrade and logs the decoded
// frames. It answers nothing else: the RPC payloads are the evidence this
// stage exists to capture.
func gatewaySink(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("gateway sink on %s: %v", addr, err)
		return
	}
	// BATTLENET_TLS_CERT/BATTLENET_TLS_KEY: terminate TLS in-process for the
	// local experiment loop (prod terminates it upstream).
	if certFile := env("BATTLENET_TLS_CERT", ""); certFile != "" {
		cert, certErr := tls.LoadX509KeyPair(certFile, env("BATTLENET_TLS_KEY", ""))
		if certErr != nil {
			log.Fatalf("gateway sink TLS: %v", certErr)
		}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}})
		log.Printf("gateway sink TLS enabled on %s", addr)
	}
	log.Printf("gateway sink on %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("gateway accept: %v", err)
			continue
		}
		go serveGateway(conn)
	}
}

// serveGateway is per-connection: a session, then the upgrade + frames. All
// writes go through the session's writer goroutine so replies and scheduled
// frames (the captured KeepAlive/logon_timeout timers) never interleave.
func serveGateway(c net.Conn) {
	ses := newGatewaySession(c.RemoteAddr().String())
	defer c.Close()
	defer close(ses.done)
	remote := ses.remote
	sinkTrace := env("BATTLENET_SINK_TRACE", "") != ""
	go func() {
		for {
			select {
			case <-ses.done:
				return
			case frame := <-ses.out:
				if sinkTrace {
					log.Printf("SINKTX %s: %x", remote, frame)
				}
				// frame is the bnet RPC packet; the WebSocket envelope goes on
				// here, at the single point every native write funnels through.
				if _, err := c.Write(encodeWSFrame(2, frame)); err != nil {
					log.Printf("GATEWAY %s write: %v", ses.remote, err)
					return
				}
			}
		}
	}()
	log.Printf("GATEWAY connect from %s", remote)

	c.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Read until the blank line of the upgrade request.
	var request []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(request, []byte("\r\n\r\n")) {
		n, err := c.Read(buf)
		if err != nil {
			log.Printf("GATEWAY %s closed during upgrade: %v", remote, err)
			return
		}
		request = append(request, buf[:n]...)
		if len(request) > 16<<10 {
			log.Printf("GATEWAY %s: oversized upgrade request", remote)
			return
		}
	}
	log.Printf("GATEWAY upgrade request:\n%s", request)

	key := websocketAcceptKey(request)
	if key == "" {
		log.Printf("GATEWAY %s: not a websocket upgrade", remote)
		return
	}

	// Complete the handshake: 101 with the accept key and the requested
	// subprotocol echoed. The reason phrase is the real gateway's ("Web
	// Socket Protocol Handshake" — captured 2026-09-14); the client accepted
	// our "Switching Protocols" too, but byte-faithful is the doctrine.
	// The D2R-services connection (LoginResponse field 1 + "/OSI/v2/rpc/client")
	// shares this listener; its subprotocol, if any, is echoed.
	d2r := bytes.Contains(bytes.SplitN(request, []byte("\r\n"), 2)[0], []byte("/v2/rpc/client"))
	protocol := "v1.rpc.battle.net"
	if d2r {
		protocol = headerValue(request, "sec-websocket-protocol:")
	}
	accept := wsAccept(key)
	response := "HTTP/1.1 101 Web Socket Protocol Handshake\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n"
	if protocol != "" {
		response += "Sec-WebSocket-Protocol: " + protocol + "\r\n"
	}
	if _, err := c.Write([]byte(response + "\r\n")); err != nil {
		return
	}
	log.Printf("GATEWAY %s: websocket established (%q, d2r services: %v)", remote, protocol, d2r)

	// Frames: client frames are masked, so unmask before decoding. Binary
	// frames carry the bnet RPC packet: u16 BE header_size + bnet.protocol
	// Header + message. ConnectionService.Connect (service 0, method 1) is
	// answered so the client proceeds to its next request — each new frame is
	// the next protocol capture.
	var pending []byte
	for {
		c.SetReadDeadline(time.Now().Add(120 * time.Second))
		n, err := c.Read(buf)
		if n > 0 {
			if sinkTrace {
				if bytes.Contains(buf[:n], []byte("eyJ")) {
					log.Printf("SINKRX %s: [redacted authentication frame, %d bytes]", remote, n)
				} else {
					log.Printf("SINKRX %s: %x", remote, buf[:n])
				}
			}
			pending = append(pending, buf[:n]...)
			for len(pending) >= 2 {
				frame, consumed := decodeWSFrame(pending)
				if consumed == 0 {
					break
				}
				pending = pending[consumed:]

				switch frame.opcode {
				case 8: // close
					log.Printf("GATEWAY %s: close frame", remote)
					return
				case 9: // ping
					c.Write(encodeWSFrame(10, frame.payload))
					log.Printf("GATEWAY %s: ping answered", remote)
					continue
				case 2: // binary
					if d2r {
						if !handleD2RServices(ses, frame.payload) {
							return
						}
						continue
					}
					if bytes.Contains(frame.payload, []byte("eyJ")) {
						log.Printf("GATEWAY rpc in (%d bytes): [authentication payload redacted]", len(frame.payload))
					} else {
						log.Printf("GATEWAY rpc in (%d bytes): %x", len(frame.payload), frame.payload)
					}
					h := parseRPCHeader(frame.payload)
					log.Printf("GATEWAY rpc header: service=%d method=%d request=%d",
						h.serviceID, h.methodID, h.requestID)
					if env("GATEWAY_RELAY", "") == "" {
						// Native mode: the ConnectionService state machine
						// answers on the captured schedule, identity resolves
						// through the account core, and every authenticated
						// frame is captured whole.
						if !handleNativeRPC(ses, frame.payload) {
							return
						}
						continue
					}
					if relayAddr := env("GATEWAY_RELAY", ""); relayAddr != "" {
						// Bidirectional relay: the real gateway keeps the
						// session alive with server-initiated frames after the
						// ConnectResponse, and the client answers them — both
						// directions are piped and logged.
						up, uerr := dialRealGateway(relayAddr)
						if uerr != nil {
							log.Printf("GATEWAY relay dial error: %v", uerr)
							continue
						}
						log.Printf("GATEWAY relay established to %s", relayAddr)

						if _, err := up.Write(encodeWSFrame(2, frame.payload)); err != nil {
							log.Printf("GATEWAY relay send error: %v", err)
							up.Close()
							continue
						}

						go pumpFrames(c, up, "client->upstream")
						pumpFrames(up, c, "upstream->client")
						up.Close()
						c.Close()
						log.Printf("GATEWAY relay session ended for %s", remote)
						return
					}
					if h.serviceID == 0 && h.methodID == 1 {
						resp := buildConnectResponse(h.requestID)
						c.Write(encodeWSFrame(2, resp))
						log.Printf("GATEWAY ConnectResponse sent (%d bytes)", len(resp))
					} else {
						log.Printf("GATEWAY unhandled rpc service=%d method=%d — captured only",
							h.serviceID, h.methodID)
					}
				default:
					log.Printf("GATEWAY frame opcode=%d len=%d", frame.opcode, len(frame.payload))
				}
			}
		}
		if err != nil {
			log.Printf("GATEWAY %s closed (%v)", remote, err)
			return
		}
	}
}

// rpcHeader carries the header fields the routing decision needs, and the
// message body that follows the header.
type rpcHeader struct {
	serviceID   uint64
	methodID    uint64
	requestID   uint64
	serviceHash uint32 // field 11: FNV-1a of the target service's name
	body        []byte
}

// parseRPCHeader reads the bnet.protocol.Header fields out of an RPC packet
// (u16 BE header size, then the header protobuf).
func parseRPCHeader(packet []byte) rpcHeader {
	var h rpcHeader
	if len(packet) < 2 {
		return h
	}
	size := int(binary.BigEndian.Uint16(packet[:2]))
	if size > len(packet)-2 {
		size = len(packet) - 2
	}
	body := packet[2 : 2+size]
	h.body = packet[2+size:]

	for i := 0; i < len(body); {
		tag := body[i]
		field, wire := tag>>3, tag&7
		i++
		switch wire {
		case 0: // varint
			var v uint64
			var shift uint
			for i < len(body) {
				b := body[i]
				i++
				v |= uint64(b&0x7f) << shift
				if b&0x80 == 0 {
					break
				}
				shift += 7
			}
			switch field {
			case 1:
				h.serviceID = v
			case 2:
				h.methodID = v
			case 3:
				h.requestID = v
			}
		case 1: // fixed64
			i += 8
		case 5: // fixed32
			if field == 11 && i+4 <= len(body) {
				h.serviceHash = binary.LittleEndian.Uint32(body[i:])
			}
			i += 4
		case 2: // length-delimited
			var l uint64
			var shift uint
			for i < len(body) {
				b := body[i]
				i++
				l |= uint64(b&0x7f) << shift
				if b&0x80 == 0 {
					break
				}
				shift += 7
			}
			i += int(l)
		default:
			return h
		}
	}
	return h
}

// dialRealGateway opens a TLS+WebSocket connection to the real gateway and
// completes the upgrade, returning the live connection to pump frames over.
func dialRealGateway(addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	raw, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}})

	upgrade := "GET / HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: T8R9IHDd6D5Hytoe5Khkpg==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: v1.rpc.battle.net\r\n\r\n"
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := conn.Write([]byte(upgrade)); err != nil {
		conn.Close()
		return nil, err
	}

	head := make([]byte, 0, 1024)
	one := make([]byte, 1)
	for !bytes.Contains(head, []byte("\r\n\r\n")) {
		n, err := conn.Read(one)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("relay upgrade: %w", err)
		}
		head = append(head, one[:n]...)
		if len(head) > 16<<10 {
			conn.Close()
			return nil, fmt.Errorf("relay upgrade: oversized response")
		}
	}
	if !bytes.Contains(head, []byte("101")) {
		conn.Close()
		return nil, fmt.Errorf("relay upgrade rejected: %q", head)
	}
	// The upstream's response headers are evidence: anything the real
	// gateway sets beyond the 101 basics is something a native handshake
	// may have to reproduce.
	log.Printf("RELAY upstream upgrade response:\n%s", head)

	conn.SetDeadline(time.Time{})
	return conn, nil
}

// pumpFrames reads WS frames from src and writes them to dst, logging each.
// Client-originated frames arrive masked and are re-emitted unmasked.
func pumpFrames(src, dst net.Conn, label string) {
	var pending []byte
	buf := make([]byte, 16<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for len(pending) >= 2 {
				f, used := decodeWSFrame(pending)
				if used == 0 {
					break
				}
				pending = pending[used:]
				if f.opcode == 2 {
					h := parseRPCHeader(f.payload)
					log.Printf("RELAY %s rpc service=%d method=%d request=%d (%d bytes): %x",
						label, h.serviceID, h.methodID, h.requestID, len(f.payload), f.payload)
				} else if f.opcode != 10 { // pongs are noise; pings and closes are evidence
					log.Printf("RELAY %s frame opcode=%d len=%d", label, f.opcode, len(f.payload))
				}
				if _, werr := dst.Write(encodeWSFrame(f.opcode, f.payload)); werr != nil {
					log.Printf("RELAY %s write: %v", label, werr)
					return
				}
				if f.opcode == 8 {
					return
				}
			}
		}
		if err != nil {
			log.Printf("RELAY %s ended: %v", label, err)
			return
		}
	}
}

func pbVarint(field uint64, v uint64) []byte {
	out := []byte{byte(field<<3 | 0)}
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

// buildConnectResponse answers ConnectionService.Connect with a minimal
// ConnectResponse. Responses mark the header service 0xFE and echo the
// request id; the client treats status 0 as success and moves to logon.
func buildConnectResponse(requestID uint64) []byte {
	header := append(pbVarint(1, 0xFE), pbVarint(2, 1)...)
	header = append(header, pbVarint(3, requestID)...)
	header = append(header, pbVarint(6, 0)...) // status: OK

	// ConnectResponse{ connect_id: 1 }
	message := pbVarint(2, 1)

	out := make([]byte, 2, 2+len(header)+len(message))
	binary.BigEndian.PutUint16(out, uint16(len(header)))
	out = append(out, header...)
	return append(out, message...)
}

func encodeWSFrame(opcode byte, payload []byte) []byte {
	out := []byte{0x80 | opcode} // fin + opcode; server frames are unmasked
	n := len(payload)
	switch {
	case n < 126:
		out = append(out, byte(n))
	case n <= 0xffff:
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, 127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(n))
		out = append(out, ext...)
	}
	return append(out, payload...)
}

func websocketAcceptKey(request []byte) string {
	return headerValue(request, "sec-websocket-key:")
}

// headerValue returns the value of the named request header (lowercase name
// with its colon), or "".
func headerValue(request []byte, name string) string {
	for _, line := range strings.Split(string(request), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), name) {
			return strings.TrimSpace(line[len(name):])
		}
	}
	return ""
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type wsFrame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// decodeWSFrame parses one client frame (masked) and returns the number of
// bytes consumed, or 0 when more bytes are needed.
func decodeWSFrame(buf []byte) (wsFrame, int) {
	if len(buf) < 2 {
		return wsFrame{}, 0
	}
	fin := buf[0]&0x80 != 0
	opcode := buf[0] & 0x0F
	masked := buf[1]&0x80 != 0
	length := uint64(buf[1] & 0x7F)
	offset := 2

	switch length {
	case 126:
		if len(buf) < offset+2 {
			return wsFrame{}, 0
		}
		length = uint64(binary.BigEndian.Uint16(buf[offset:]))
		offset += 2
	case 127:
		if len(buf) < offset+8 {
			return wsFrame{}, 0
		}
		length = binary.BigEndian.Uint64(buf[offset:])
		offset += 8
	}

	var maskKey []byte
	if masked {
		if len(buf) < offset+4 {
			return wsFrame{}, 0
		}
		maskKey = buf[offset : offset+4]
		offset += 4
	}

	if uint64(len(buf)-offset) < length {
		return wsFrame{}, 0
	}
	payload := buf[offset : offset+int(length)]
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return wsFrame{fin: fin, opcode: opcode, payload: payload}, offset + int(length)
}
