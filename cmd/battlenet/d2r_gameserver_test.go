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
	server, client := net.Pipe()
	go serveGame(server)

	hello := make([]byte, 2)
	if _, err := io.ReadFull(client, hello); err != nil || hello[0] != 0xAF || hello[1] != 0x00 {
		t.Fatalf("hello = %x %v", hello, err)
	}
	logon := make([]byte, 45)
	logon[0] = 0x68
	binary.LittleEndian.PutUint32(logon[1:5], 7)
	binary.LittleEndian.PutUint16(logon[5:7], 7)
	logon[7] = 4
	copy(logon[29:], "Hansolo")
	if _, err := client.Write(logon); err != nil {
		t.Fatal(err)
	}
	flags := make([]byte, 9)
	if _, err := io.ReadFull(client, flags); err != nil {
		t.Fatal(err)
	}
	if flags[0] != 0x01 || binary.LittleEndian.Uint32(flags[2:6])&0x4 == 0 || flags[8] != 0x00 {
		t.Fatalf("flags frame = %x", flags)
	}
	ping := make([]byte, 13)
	ping[0] = 0x6D
	binary.LittleEndian.PutUint32(ping[1:5], 1234)
	if _, err := client.Write(ping); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 34)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x02 || reply[1] != 0x8F || binary.LittleEndian.Uint32(reply[2:6]) != 1234 {
		t.Fatalf("after ping = %x", reply)
	}
	if _, err := client.Write([]byte{0x6B}); err != nil {
		t.Fatal(err)
	}
	burst := make([]byte, 8+12+6*len(revealCells)+71+6+1)
	if _, err := io.ReadFull(client, burst); err != nil {
		t.Fatal(err)
	}
	ids := []byte{burst[0], burst[8], burst[20], burst[20+6*len(revealCells)], burst[91+6*len(revealCells)], burst[97+6*len(revealCells)]}
	if string(ids) != string([]byte{0x01, 0x03, 0x07, 0x59, 0x0B, 0x04}) {
		t.Fatalf("burst ids = %x", ids)
	}
	if burst[20+6*len(revealCells)+5] != 4 || string(burst[20+6*len(revealCells)+6:20+6*len(revealCells)+13]) != "Hansolo" {
		t.Fatalf("assign player = %x", burst[20+6*len(revealCells):20+6*len(revealCells)+71])
	}
	client.Close()
}
