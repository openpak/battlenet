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
	rememberPendingJoin(999, 7) // unknown character: falls back to "Player"
	server, client := net.Pipe()
	go serveGame(server)

	hello := make([]byte, 2)
	if _, err := io.ReadFull(client, hello); err != nil || hello[0] != 0xAF || hello[1] != 0x00 {
		t.Fatalf("hello = %x %v", hello, err)
	}
	burst := make([]byte, 8+12+6*len(revealCells)+71+6+1)
	if _, err := io.ReadFull(client, burst); err != nil {
		t.Fatal(err)
	}
	ids := []byte{burst[0], burst[8], burst[20], burst[20+6*len(revealCells)], burst[91+6*len(revealCells)], burst[97+6*len(revealCells)]}
	if string(ids) != string([]byte{0x01, 0x03, 0x07, 0x59, 0x0B, 0x04}) {
		t.Fatalf("burst ids = %x", ids)
	}
	if burst[6] != 1 {
		t.Fatalf("edition byte must be 1..3, got %d", burst[6])
	}
	assign := burst[20+6*len(revealCells):]
	if string(assign[6:12]) != "Player" {
		t.Fatalf("assign player = %x", assign[:71])
	}
	ping := make([]byte, 13)
	ping[0] = 0x6D
	binary.LittleEndian.PutUint32(ping[1:5], 1234)
	if _, err := client.Write(ping); err != nil {
		t.Fatal(err)
	}
	pong := make([]byte, 33)
	if _, err := io.ReadFull(client, pong); err != nil || pong[0] != 0x8F || binary.LittleEndian.Uint32(pong[1:5]) != 1234 {
		t.Fatalf("pong = %x %v", pong, err)
	}
	client.Close()
}
