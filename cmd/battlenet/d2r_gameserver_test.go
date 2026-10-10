package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// The one check: the server speaks first, accepts the logon, answers flags with
// bit 0x4, then `00`, then `02` after the first ping and pongs it.
func TestGameServerHandshake(t *testing.T) {
	rememberPendingJoin("", 999, 7) // unknown character: falls back to "Player"
	server, client := net.Pipe()
	go serveGame(server)

	hello := make([]byte, 2+33)
	if _, err := io.ReadFull(client, hello); err != nil || hello[0] != 0xAF || hello[1] != 0x00 || hello[2] != 0x8F {
		t.Fatalf("hello must be AF 00 then an unrequested pong, got %x %v", hello, err)
	}
	// Nothing else until the client pings: the first pong must precede the load.
	ping := make([]byte, 13)
	ping[0] = 0x6D
	binary.LittleEndian.PutUint32(ping[1:5], 1234)
	if _, err := client.Write(ping); err != nil {
		t.Fatal(err)
	}
	sizes := map[byte]int{0x8F: 33, 0x01: 8, 0x00: 1, 0x03: 12, 0x07: 6, 0x59: 71, 0x0B: 6, 0x1E: 7, 0x1F: 9, 0x04: 1}
	var ids []byte
	first := true
	for len(ids) == 0 || ids[len(ids)-1] != 0x04 || first {
		head := make([]byte, 1)
		if _, err := io.ReadFull(client, head); err != nil {
			t.Fatal(err)
		}
		n, ok := sizes[head[0]]
		if !ok {
			t.Fatalf("unexpected server packet %#x after %x", head[0], ids)
		}
		body := make([]byte, n-1)
		if _, err := io.ReadFull(client, body); err != nil {
			t.Fatal(err)
		}
		if first {
			if head[0] != 0x8F || binary.LittleEndian.Uint32(body[0:4]) != 1234 {
				t.Fatalf("first packet after the ping must be its pong, got %#x %x", head[0], body)
			}
			first = false
		}
		if head[0] == 0x01 && body[5] != 1 {
			t.Fatalf("edition byte must be 1..3, got %d", body[5])
		}
		if head[0] == 0x59 && string(body[5:11]) != "Player" {
			t.Fatalf("assign player = %x", body)
		}
		ids = append(ids, head[0])
	}
	want := []byte{0x8F, 0x01, 0x00, 0x8F, 0x03, 0x8F, 0x07, 0x07, 0x07, 0x07, 0x07, 0x07, 0x07, 0x07, 0x07, 0x8F, 0x59, 0x8F, 0x0B, 0x8F, 0x1E, 0x1E, 0x1E, 0x1E, 0x1E, 0x1F, 0x1F, 0x1F, 0x1F, 0x1F, 0x1F, 0x8F, 0x04}
	if string(ids) != string(want) {
		t.Fatalf("sequence = %x, want %x", ids, want)
	}
	trailing := make([]byte, 33)
	if _, err := io.ReadFull(client, trailing); err != nil || trailing[0] != 0x8F {
		t.Fatalf("pong after 04 missing: %x %v", trailing, err)
	}
	npcs := make([]byte, 14*len(townNPCs))
	if _, err := io.ReadFull(client, npcs); err != nil || npcs[0] != 0xAC || npcs[12] != 0x0e || npcs[13] != 0x01 || npcs[14] != 0xAC {
		t.Fatalf("npc assignments = %x %v", npcs, err)
	}
	// a long walk packet must be consumed whole, then talking to an NPC opens its dialog
	walk := make([]byte, 210)
	walk[0] = 0x01
	talk := []byte{0x2F, 2, 0, 0, 0}
	if _, err := client.Write(append(walk, talk...)); err != nil {
		t.Fatal(err)
	}
	dialog := make([]byte, 40+103)
	if _, err := io.ReadFull(client, dialog); err != nil || dialog[0] != 0x27 || dialog[2] != 2 || dialog[40] != 0x28 || dialog[41] != 1 || dialog[42] != 2 {
		t.Fatalf("dialog = %x %v", dialog[:8], err)
	}
	client.Close()
}
