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

	t.Setenv("BATTLENET_GAMEDETAIL_TUNE", t.TempDir()+"/tune.json")
	body, _ := gameDetailBody("Game1628292938")

	// Field 1 echoes the name the client asked about, and the two markers go
	// out in distinct fields so the client's dial identifies which is the host.
	if !bytes.Contains(body, []byte("Game1628292938")) {
		t.Fatalf("name missing: %x", body)
	}
	// With no tune file the body is just the echoed name: an absent probe
	// must not invent field values.
	if !bytes.Equal(body, pbBytes(1, []byte("Game1628292938"))) {
		t.Fatalf("untuned body should be the name alone, got %x", body)
	}
}
