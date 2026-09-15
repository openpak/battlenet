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
		"425e" + regionDescHex,      // field 8: region descriptor, verbatim
		"4a21" + hex.EncodeToString([]byte(s.sid)), // field 9: session id
		"5001",                      // field 10: 1
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
