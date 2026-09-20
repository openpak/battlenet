package main

import (
	"bytes"
	"testing"
)

// The bytes the live client sends: 0a0e "Game1628292938".
func TestGameDetailProbeEchoesTheName(t *testing.T) {
	req := pbBytes(1, []byte("Game1628292938"))

	if got := gameDetailName(req); got != "Game1628292938" {
		t.Fatalf("name = %q", got)
	}

	body := gameDetailBody("Game1628292938")

	// Field 1 echoes the name the client asked about, and the two markers go
	// out in distinct fields so the client's dial identifies which is the host.
	if !bytes.Contains(body, []byte("Game1628292938")) {
		t.Fatalf("name missing: %x", body)
	}
	if !bytes.Contains(body, pbBytes(2, []byte(probeHostField2))) {
		t.Fatalf("field 2 marker missing: %x", body)
	}
	if !bytes.Contains(body, pbBytes(5, []byte(probeHostField5))) {
		t.Fatalf("field 5 marker missing: %x", body)
	}
	if probeHostField2 == probeHostField5 {
		t.Fatal("markers must differ or the probe cannot discriminate")
	}
}
