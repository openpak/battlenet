package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// joinTestGame dials a game server over a pipe, answers its opening and first
// ping, and reads until the join burst and townsfolk are through.
func joinTestGame(t *testing.T, gameID uint32, name string) net.Conn {
	t.Helper()
	gameMu.Lock()
	rec := &gameRecord{ID: gameID, Seated: map[string]int{}}
	gamesByKey[name] = rec
	gameMu.Unlock()
	pendingMu.Lock()
	pendingJoins = append(pendingJoins[:0], pendingJoin{name: name, class: 1, edition: 2, gameID: gameID, subject: name, at: time.Now()})
	pendingMu.Unlock()
	server, client := net.Pipe()
	go serveGame(server)
	readN(t, client, 35)
	ping := make([]byte, 13)
	ping[0] = 0x6D
	if _, err := client.Write(ping); err != nil {
		t.Fatal(err)
	}
	// pong, burst, then one AC per townsperson
	for {
		id := readN(t, client, 1)[0]
		if id == 0xAC {
			readN(t, client, 14*len(townNPCs)-1)
			return client
		}
		readN(t, client, map[byte]int{0x8F: 33, 0x01: 8, 0x00: 1, 0x03: 12, 0x07: 6, 0x59: 71, 0x0B: 6, 0x1E: 7, 0x1F: 9, 0x04: 1}[id]-1)
	}
}

func readN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("read %d: %v", n, err)
	}
	return b
}

// Two players in one game: each is assigned to the other, movement reaches
// the other one only, and leaving removes the unit.
func TestTwoPlayersShareTown(t *testing.T) {
	a := joinTestGame(t, 4242, "Alpha")
	b := joinTestGame(t, 4242, "Bravo")

	// B learns about A, A about B: 59 with the other's id and name.
	gotA := readN(t, b, 71)
	gotB := readN(t, a, 71)
	if gotA[0] != 0x59 || string(gotA[6:11]) != "Alpha" || binary.LittleEndian.Uint32(gotA[1:5]) != 1 {
		t.Fatalf("B's view of A = %x", gotA[:12])
	}
	if gotB[0] != 0x59 || string(gotB[6:11]) != "Bravo" || binary.LittleEndian.Uint32(gotB[1:5]) != 2 {
		t.Fatalf("A's view of B = %x", gotB[:12])
	}

	// A moves with the stick: B gets 24 for unit 1 at the reported point.
	move := []byte{0x42, 0x80, 0x79, 0x16, 0x16, 0x94, 0x15}
	if _, err := a.Write(move); err != nil {
		t.Fatal(err)
	}
	relay := readN(t, b, 13)
	if relay[0] != 0x24 || binary.LittleEndian.Uint32(relay[2:6]) != 1 || binary.LittleEndian.Uint16(relay[9:11]) != 0x1616 || binary.LittleEndian.Uint16(relay[11:13]) != 0x1594 {
		t.Fatalf("analog relay = %x", relay)
	}

	// B runs to a point: A gets 0F, mode run.
	run := make([]byte, 210)
	run[0] = 0x03
	binary.LittleEndian.PutUint16(run[1:3], 5660)
	binary.LittleEndian.PutUint16(run[3:5], 5530)
	binary.LittleEndian.PutUint16(run[5:7], spawnX)
	binary.LittleEndian.PutUint16(run[7:9], spawnY)
	if _, err := b.Write(run); err != nil {
		t.Fatal(err)
	}
	walk := readN(t, a, 16)
	if walk[0] != 0x0F || binary.LittleEndian.Uint32(walk[2:6]) != 2 || walk[6] != 0x17 || binary.LittleEndian.Uint16(walk[7:9]) != 5660 {
		t.Fatalf("walk relay = %x", walk)
	}

	// A third player sees both where they last were.
	c := joinTestGame(t, 4242, "Charlie")
	for i := 0; i < 2; i++ {
		got := readN(t, c, 71)
		switch binary.LittleEndian.Uint32(got[1:5]) {
		case 1:
			if binary.LittleEndian.Uint16(got[67:69]) != 0x1616 {
				t.Fatalf("A's position for C = %x", got[67:71])
			}
		case 2:
			if binary.LittleEndian.Uint16(got[67:69]) != 5660 {
				t.Fatalf("B's position for C = %x", got[67:71])
			}
		default:
			t.Fatalf("C got %x", got[:6])
		}
	}
	readN(t, a, 71)
	readN(t, b, 71)

	// B leaves: A and C drop unit 2.
	b.Close()
	for _, peer := range []net.Conn{a, c} {
		gone := readN(t, peer, 6)
		if gone[0] != 0x0A || binary.LittleEndian.Uint32(gone[2:6]) != 2 {
			t.Fatalf("removal = %x", gone)
		}
	}
	a.Close()
	c.Close()
}
