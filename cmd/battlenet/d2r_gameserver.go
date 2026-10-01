package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"log"
	"net"
	"time"
)

// The Diablo II game-server transport. After GameJoin the title dials field 1 on
// TCP 443 with its own OpenSSL (trust = the IPS-patched bundle, i.e. the OpenPak
// CA; see tools/d2r-trust) and expects the game server to speak first. Traefik
// terminates TLS for d2gs.battle.net and splices the clear stream here.
//
// Wire facts (docs/d2r-protocol.md "CORRECTION 2026-09-30"): server `AF 00`;
// client `68` logon (45 bytes: u32 hash = GameJoin field 3, u16 token = field 4,
// u8 class, three constants, u8 0, u64 character id, char[16] name); server `01`
// game flags (i8 difficulty, u32 flags with bit 0x4 set and difficulty /
// expansion / ladder bits self-consistent, i8 expansion, i8 ladder); server
// `00`; client `6D` ping (13 bytes); server `02`; then pongs `8F` and in-game
// packets. The client leaves "creating game" only once complete in-game packets
// arrive every < 15 s -- that part is the game server still to be written.
//
// ponytail: handshake plus the minimal join burst for one hard-coded town seed,
// pongs every ping, logs everything else. No units, no simulation yet.

const (
	d2gsFlagBase      = 0x4
	d2gsFlagExpansion = 0x100000
)

func gameServer(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("game server on %s: %v", addr, err)
		return
	}
	log.Printf("game server on %s (d2gs.battle.net handshake)", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("D2GS accept: %v", err)
			continue
		}
		go serveGame(conn)
	}
}

// serveGame walks one connection through the handshake and the join burst.
func serveGame(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("D2GS %s connected", remote)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))

	if _, err := conn.Write([]byte{0xAF, 0x00}); err != nil {
		return
	}

	// Client packets are delimited by their first byte. The game loop pings
	// (6D) from the moment the socket is open, before and after the logon, and
	// the update build drops the connection if a ping is unanswered for 5 s,
	// so pongs go out immediately whatever state we are in.
	sizes := map[byte]int{0x68: 45, 0x6D: 13, 0x6B: 1, 0x69: 1}
	var flags []byte
	var name string
	var class byte
	r := bufio.NewReader(conn)
	sentStart, inGame := false, false
	for {
		id, err := r.ReadByte()
		if err != nil {
			log.Printf("D2GS %s closed: %v", remote, err)
			return
		}
		size, ok := sizes[id]
		if !ok {
			rest, _ := r.Peek(r.Buffered())
			log.Printf("D2GS %s: unknown packet %#x, %d bytes buffered after it: %x", remote, id, len(rest), rest)
			return
		}
		pkt := make([]byte, size)
		pkt[0] = id
		if _, err := io.ReadFull(r, pkt[1:]); err != nil {
			log.Printf("D2GS %s: short packet %#x: %v", remote, id, err)
			return
		}
		switch id {
		case 0x6D:
			pong := make([]byte, 33)
			pong[0] = 0x8F
			copy(pong[1:5], pkt[1:5])
			if _, err := conn.Write(pong); err != nil {
				return
			}
			if flags != nil && !sentStart {
				sentStart = true
				if _, err := conn.Write([]byte{0x02}); err != nil {
					return
				}
			}
		case 0x68:
			hash := binary.LittleEndian.Uint32(pkt[1:5])
			token := binary.LittleEndian.Uint16(pkt[5:7])
			charID := binary.LittleEndian.Uint64(pkt[21:29])
			class = pkt[7]
			name = string(trimNUL(pkt[29:45]))
			log.Printf("D2GS %s: logon hash=%d token=%d class=%d character=%d name=%q", remote, hash, token, class, charID, name)
			flags = make([]byte, 8)
			flags[0] = 0x01
			flags[1] = 0 // Normal
			binary.LittleEndian.PutUint32(flags[2:6], d2gsFlagBase|d2gsFlagExpansion)
			flags[6] = 1 // edition
			flags[7] = 0 // ladder
			if _, err := conn.Write(append(append([]byte{}, flags...), 0x00)); err != nil {
				return
			}
		case 0x6B:
			if flags == nil || inGame {
				continue
			}
			inGame = true
			if _, err := conn.Write(joinBurst(class, name, flags)); err != nil {
				return
			}
			log.Printf("D2GS %s: join burst sent (seed %#x, area %d)", remote, townSeed, townArea)
		case 0x69:
			log.Printf("D2GS %s: client left the game", remote)
		}
	}
}

// A known (seed, spawn) pair for the Act I town: from a published 1.13 capture
// (private scratch 3.3.4/4.2). The
// client generates the level from the seed; the reveal cells and the player
// position must fall inside rooms that exist for THIS seed or the client
// crashes, so these four numbers travel together. The map generator is the
// same in D2R (ponytail: one hard-coded town until we run a generator).
const (
	townSeed  = 0x01403123
	townSeed2 = 0x4D4FF68B
	townArea  = 1    // Rogue Encampment
	spawnX    = 5653 // subtiles
	spawnY    = 5523
	playerID  = 1
)

var revealCells = [][2]uint16{
	{0x460, 0x448}, {0x468, 0x448}, {0x470, 0x448},
	{0x460, 0x450}, {0x468, 0x450}, {0x470, 0x450},
	{0x460, 0x458}, {0x468, 0x458}, {0x470, 0x458},
}

// joinBurst is the minimal in-game sequence the update build needs to put the
// local player in town (ingame-packets.md section 2, update layouts): 01 flags
// (the handshake copy does not reach the game), 03 load act, 07 reveals, 59
// assign player (71 bytes), 0B set local player, 04 load complete.
func joinBurst(class byte, name string, flags []byte) []byte {
	out := append([]byte{}, flags[:8]...)
	act := make([]byte, 12)
	act[0] = 0x03
	act[1] = 0
	binary.LittleEndian.PutUint32(act[2:6], townSeed)
	binary.LittleEndian.PutUint16(act[6:8], townArea)
	binary.LittleEndian.PutUint32(act[8:12], townSeed2)
	out = append(out, act...)
	for _, c := range revealCells {
		r := make([]byte, 6)
		r[0] = 0x07
		binary.LittleEndian.PutUint16(r[1:3], c[0])
		binary.LittleEndian.PutUint16(r[3:5], c[1])
		r[5] = townArea
		out = append(out, r...)
	}
	assign := make([]byte, 71)
	assign[0] = 0x59
	binary.LittleEndian.PutUint32(assign[1:5], playerID)
	assign[5] = class
	copy(assign[6:66], name) // NUL-terminated, at most 60 used
	binary.LittleEndian.PutUint16(assign[67:69], spawnX)
	binary.LittleEndian.PutUint16(assign[69:71], spawnY)
	out = append(out, assign...)
	local := make([]byte, 6)
	local[0] = 0x0B
	binary.LittleEndian.PutUint32(local[2:6], playerID)
	out = append(out, local...)
	return append(out, 0x04)
}

func trimNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
