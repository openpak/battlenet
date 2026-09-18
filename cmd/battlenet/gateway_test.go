package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

// The generated frames must be byte-identical to what the real gateway sent
// (2026-09-14 relay sessions, session DC4EAA6F6A8CD149-00000000000C3783),
// modulo the values that are legitimately per-session. The test freezes the
// clock and the session identity to that capture and compares whole frames.
func TestFrameFidelity(t *testing.T) {
	frozen := time.Unix(1789386438, 500000000) // 2026-09-14 11:47:18.5 UTC
	now = func() time.Time { return frozen }
	defer func() { now = time.Now }()

	s := newGatewaySession("test")
	s.a = 0xdc4eaa6f
	s.c = 0xc3783
	s.sid = "DC4EAA6F6A8CD149-00000000000C3783"

	// Captured ConnectResponse, fresh from the same session. Its embedded
	// clock differs (captured 11:47:17.57 vs frozen 11:47:18.5 — the µs
	// clock moves), so compare structure: header, field 8/9/10 verbatim,
	// and field 2's unix-seconds component. The µs fields are checked for
	// plausibility instead.
	got := s.buildConnectResponse()
	if hs := int(binary.BigEndian.Uint16(got[:2])); hs != 10 {
		t.Fatalf("ConnectResponse header size %d, want 10", hs)
	}
	if !bytes.Equal(got[2:8], []byte{0x08, 0xfe, 0x01, 0x18, 0x00, 0x28}) {
		t.Fatalf("ConnectResponse header prefix: %x", got[2:8])
	}
	hexFull := hex.EncodeToString(got)
	for _, want := range []string{
		"425e" + regionDescHex,                     // field 8: region descriptor, verbatim
		"4a21" + hex.EncodeToString([]byte(s.sid)), // field 9: session id
		"5001", // field 10: 1
	} {
		if !bytes.Contains(got, mustHex(want)) {
			t.Fatalf("ConnectResponse missing %s:\n%x", want, got)
		}
	}
	// The µs clock must be within a minute of the frozen wall clock.
	if !bytes.Contains(got, pbVarint(6, uint64(frozen.UnixMicro()))) {
		t.Fatalf("ConnectResponse field 6 (µs clock) wrong:\n%x", got)
	}
	t.Logf("ConnectResponse (%d bytes): %s", len(got), hexFull)

	// method 12: identical to the capture with frozen time.
	method12 := append(pbVarint(1, 1), pbVarint(2, uint64(frozen.UnixMicro()))...)
	method12 = append(method12, pbVarint(3, 0)...)
	got12 := s.buildServerRPC(12, 2, 13, method12)
	want12 := "00300800100c1802280d5d350e24716a21444334454141364636413843443134392d30303030303030303030304333373833080110" +
		hex.EncodeToString(pbLen(uint64(frozen.UnixMicro()))) + "1800"
	if hex.EncodeToString(got12) != want12 {
		t.Fatalf("method12 mismatch:\n got %x\nwant %s", got12, want12)
	}

	// method 13 and method 5: captured verbatim.
	got13 := s.buildServerRPC(13, 3, 0, nil)
	want13, _ := hex.DecodeString("00300800100d180328005d350e24716a21444334454141364636413843443134392d30303030303030303030304333373833")
	if !bytes.Equal(got13, want13) {
		t.Fatalf("method13 mismatch:\n got %x\nwant %x", got13, want13)
	}
	got5 := s.buildServerRPC(5, 4, 3, mustHex("088a04"))
	want5, _ := hex.DecodeString("003008001005180428035d350e24716a21444334454141364636413843443134392d30303030303030303030304333373833088a04")
	if !bytes.Equal(got5, want5) {
		t.Fatalf("method5 mismatch:\n got %x\nwant %x", got5, want5)
	}
}

func TestSuccessfulLogonResult(t *testing.T) {
	got, err := buildSuccessfulLogonResult("4152366661")
	if err != nil {
		t.Fatal(err)
	}

	account := pbBytes(2, entityID(battlenetAccountHigh, 4152366661))
	gameAccount := pbBytes(3, entityID(d2rGameAccountHigh, 4152366661))
	for name, want := range map[string][]byte{
		"success error code": {0x08, 0x00},
		"account entity id":  account,
		"game entity id":     gameAccount,
		"available region":   {0x28, 0x01},
		"connected region":   {0x30, 0x01},
		"country":            pbBytes(8, []byte("US")),
	} {
		if !bytes.Contains(got, want) {
			t.Errorf("LogonResult missing %s (%x): %x", name, want, got)
		}
	}

	// Field 9 is a length-delimited 64-byte session key.
	if !bytes.Contains(got, []byte{0x4a, 0x40}) {
		t.Fatalf("LogonResult missing 64-byte session key: %x", got)
	}
	if len(got) != 130 {
		t.Fatalf("LogonResult length %d, want 130: %x", len(got), got)
	}
}

func TestRPCResponse(t *testing.T) {
	got := buildRPCResponse(3, []byte{0x0a, 0x01, 'x'})
	want := mustHex("000708fe01180330000a0178")
	if !bytes.Equal(got, want) {
		t.Fatalf("RPC response mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestLoggedOnRouting(t *testing.T) {
	// Captured GenerateWebCredentials request: hash at header field 11,
	// body {1: program "OSI"} after the header.
	h := parseRPCHeader(mustHex("000b0800100818025d01fcec0d0d49534f00"))
	if h.serviceHash != authServerHash || h.methodID != 8 || h.requestID != 2 {
		t.Fatalf("header: %+v", h)
	}
	if !bytes.Equal(h.body, mustHex("0d49534f00")) {
		t.Fatalf("body %x", h.body)
	}
	if got := bgsName(sessionServiceHash, 1); got != "SessionService.CreateSession" {
		t.Fatalf("name %q", got)
	}

	// OnSessionCreated echoes CreateSessionRequest identity and session_key.
	identity := mustHex("0a050d451680f7")
	req := append(pbBytes(1, identity), pbFixed32(2, []byte("hcwS"))...)
	req = append(req, pbVarint(5, 1)...)
	req = append(req, pbBytes(7, []byte("key"))...)
	want := append(pbBytes(1, identity), pbVarint(2, 0)...)
	want = append(want, pbBytes(3, []byte("sid"))...)
	want = append(want, pbBytes(4, []byte("key"))...)
	if got := sessionCreated(req, "sid"); !bytes.Equal(got, want) {
		t.Fatalf("OnSessionCreated:\n got %x\nwant %x", got, want)
	}

	// ProcessTaskResponse: two v2.Attributes, the LoginResponse as a blob,
	// whose field 1 is the D2R-services base URL and field 2 the token.
	login := d2rLoginResponse("4152366661")
	for _, want := range [][]byte{
		mustHex("0a450a0f636c69656e745f726573706f6e7365"), // result{name "client_response"
		pbBytes(1, []byte("wss://us.actual.battle.net:1119")),
		pbBytes(2, []byte("openpak:4152366661")),
	} {
		if !bytes.Contains(login, want) {
			t.Fatalf("LoginResponse missing %x:\n%x", want, login)
		}
	}
}
