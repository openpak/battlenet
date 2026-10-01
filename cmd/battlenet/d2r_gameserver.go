package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
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

	// The client's first ping is sent at game start, before this connection
	// exists, so it never reaches us; at the next 5 s tick the client marks the
	// heartbeat failed for good. An unrequested pong right after the opening is
	// processed as soon as the socket is up and clears that flag in time.
	if _, err := conn.Write(append([]byte{0xAF, 0x00}, pong(nil)...)); err != nil {
		return
	}

	// The update build never identifies itself on this socket (no 0x68 on the
	// online path), so the player is whoever made the last GameJoin.
	// ponytail: one pending join for the whole box; key it by connection once
	// two players can be joining at the same time.
	join := takePendingJoin()
	flags := make([]byte, 8)
	flags[0] = 0x01
	flags[1] = 0 // Normal
	binary.LittleEndian.PutUint32(flags[2:6], d2gsFlagBase|d2gsFlagExpansion)
	flags[6] = join.edition
	flags[7] = 0 // ladder
	burstSent := false

	// Client packets are delimited by their first byte. The in-game heartbeat
	// pings (6D) every 5 s and the update build drops the connection after one
	// unanswered tick, so pongs go out immediately.
	sizes := clientPacketSizes
	r := bufio.NewReader(conn)
	for {
		id, err := r.ReadByte()
		if err != nil {
			log.Printf("D2GS %s closed: %v", remote, err)
			return
		}
		size, ok := sizes[id]
		if !ok {
			// Unknown id: log it with what follows and drop the buffered bytes
			// rather than the connection; the stream resyncs at the next read.
			rest, _ := r.Peek(r.Buffered())
			log.Printf("D2GS %s: unknown packet %#x, %d bytes buffered after it: %x", remote, id, len(rest), rest)
			r.Discard(len(rest))
			continue
		}
		pkt := make([]byte, size)
		pkt[0] = id
		if _, err := io.ReadFull(r, pkt[1:]); err != nil {
			log.Printf("D2GS %s: short packet %#x: %v", remote, id, err)
			return
		}
		switch id {
		case 0x6D:
			log.Printf("D2GS %s: ping tick=%d", remote, binary.LittleEndian.Uint32(pkt[1:5]))
			if _, err := conn.Write(pong(pkt)); err != nil {
				return
			}
			// The client pings from game init and checks for the answer inside
			// its loader too, so the burst goes out only after the first ping
			// is answered, with pongs interleaved (the one before 04 matters).
			if !burstSent {
				burstSent = true
				if _, err := conn.Write(joinBurst(join.class, join.name, flags)); err != nil {
					return
				}
				log.Printf("D2GS %s: join burst sent for %q (class %d, edition %d, game %d, seed %#x, area %d)", remote, join.name, join.class, join.edition, join.gameID, townSeed, townArea)
			}
		case 0x68:
			log.Printf("D2GS %s: logon packet (unexpected on this build): class=%d name=%q", remote, pkt[7], trimNUL(pkt[29:45]))
		case 0x6B:
			log.Printf("D2GS %s: client reports loaded", remote)
		case 0x69:
			log.Printf("D2GS %s: client left the game", remote)
		case 0x6F:
		default:
			log.Printf("D2GS %s: client packet %#x %x", remote, id, pkt[1:])
		}
	}
}

// clientPacketSizes: lengths including the id byte. Classic ids from the public
// D2GS table (private scratch), plus the
// handshake ids and the console build's 0x42 position report (observed: id +
// 6 bytes). Ids the update build changed are corrected as they are found.
var clientPacketSizes = map[byte]int{
	0x01: 5, 0x02: 9, 0x03: 5, 0x04: 9, 0x05: 5, 0x06: 9, 0x07: 9, 0x08: 5, 0x09: 9, 0x0A: 9,
	0x0B: 1, 0x0C: 5, 0x0D: 9, 0x0E: 9, 0x0F: 5, 0x10: 9, 0x11: 9, 0x12: 1, 0x13: 9,
	0x16: 13, 0x17: 5, 0x18: 17, 0x19: 5, 0x1A: 9, 0x1B: 9, 0x1C: 3, 0x1D: 9, 0x1E: 9, 0x1F: 17,
	0x20: 13, 0x21: 9, 0x22: 5, 0x23: 9, 0x24: 5, 0x25: 9, 0x26: 13, 0x27: 9, 0x28: 9, 0x29: 9, 0x2A: 9,
	0x2F: 9, 0x30: 9, 0x31: 9, 0x32: 17, 0x33: 17, 0x34: 5, 0x35: 17, 0x36: 9, 0x37: 5, 0x38: 13,
	0x39: 5, 0x3A: 3, 0x3B: 3, 0x3C: 9, 0x3D: 5, 0x3E: 5, 0x3F: 3, 0x40: 1, 0x41: 1, 0x42: 7,
	0x44: 17, 0x45: 9, 0x46: 13, 0x47: 13, 0x48: 1, 0x49: 9, 0x4B: 9, 0x4C: 5, 0x4D: 3, 0x4F: 7,
	0x50: 9, 0x51: 9, 0x52: 5, 0x53: 1,
	0x68: 45, 0x69: 1, 0x6B: 1, 0x6D: 13, 0x6F: 5,
}

type pendingJoin struct {
	name    string
	class   byte
	edition byte // 1 classic, 2 expansion, 3 RotW: the character's, per the roster flags
	gameID  uint32
}

var (
	pendingMu sync.Mutex
	pending   *pendingJoin
)

// rememberPendingJoin notes who is about to dial the game host, from the
// roster entry the GameJoin named.
func rememberPendingJoin(characterID uint64, gameID uint32) {
	j := &pendingJoin{name: "Player", edition: 1, gameID: gameID}
	charMu.Lock()
	for _, c := range charList {
		if c.ID == characterID {
			j.name, j.class = c.Name, byte(c.Class)
			if c.Flags&0x20 != 0 {
				j.edition = 2
			}
		}
	}
	charMu.Unlock()
	pendingMu.Lock()
	pending = j
	pendingMu.Unlock()
}

func takePendingJoin() pendingJoin {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pending == nil {
		return pendingJoin{name: "Player", edition: 1}
	}
	j := *pending
	pending = nil
	return j
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
	idle := pong(nil)
	out := append(append([]byte{}, flags[:8]...), 0x00) // 00 = game loading, as the classic burst orders it
	out = append(out, idle...)
	act := make([]byte, 12)
	act[0] = 0x03
	act[1] = 0
	binary.LittleEndian.PutUint32(act[2:6], townSeed)
	binary.LittleEndian.PutUint16(act[6:8], townArea)
	binary.LittleEndian.PutUint32(act[8:12], townSeed2)
	out = append(out, act...)
	out = append(out, idle...)
	for _, c := range revealCells {
		r := make([]byte, 6)
		r[0] = 0x07
		binary.LittleEndian.PutUint16(r[1:3], c[0])
		binary.LittleEndian.PutUint16(r[3:5], c[1])
		r[5] = townArea
		out = append(out, r...)
	}
	out = append(out, idle...)
	assign := make([]byte, 71)
	assign[0] = 0x59
	binary.LittleEndian.PutUint32(assign[1:5], playerID)
	assign[5] = class
	copy(assign[6:66], name) // NUL-terminated, at most 60 used
	binary.LittleEndian.PutUint16(assign[67:69], spawnX)
	binary.LittleEndian.PutUint16(assign[69:71], spawnY)
	out = append(out, assign...)
	out = append(out, idle...)
	local := make([]byte, 6)
	local[0] = 0x0B
	binary.LittleEndian.PutUint32(local[2:6], playerID)
	out = append(out, local...)
	out = append(out, idle...)
	out = append(out, startingStats(class)...)
	out = append(out, idle...)
	return append(append(out, 0x04), idle...)
}

// Classic stat ids. Life/mana/stamina and their maxima are fixed-point with 8
// fraction bits on the wire.
const (
	statStrength   = 0
	statEnergy     = 1
	statDexterity  = 2
	statVitality   = 3
	statLife       = 6
	statMaxLife    = 7
	statMana       = 8
	statMaxMana    = 9
	statStamina    = 10
	statMaxStamina = 11
	statLevel      = 12
)

// startingStats is a level-1 character sheet. Update-build layouts: 1E = u16
// stat, u16 value, u16 param (7 bytes); 1F = u16 stat, u16 param, u32 value
// (9 bytes). Stats must follow 0B (local player set) or the client aborts.
// ponytail: one fixed sheet per class table row; real values come with saves.
func startingStats(class byte) []byte {
	// str, dex, vit, energy, life, mana, stamina per class (Amazon, Sorceress,
	// Necromancer, Paladin, Barbarian, Druid, Assassin), classic starting values.
	table := [7][7]uint16{
		{20, 25, 20, 15, 50, 15, 84},
		{10, 25, 10, 35, 40, 35, 74},
		{15, 25, 15, 25, 45, 25, 79},
		{25, 20, 25, 15, 55, 15, 89},
		{30, 20, 25, 10, 55, 10, 92},
		{15, 20, 25, 20, 55, 20, 84},
		{20, 20, 20, 25, 50, 25, 95},
	}
	row := table[4]
	if int(class) < len(table) {
		row = table[class]
	}
	u16 := func(stat, value uint16) []byte {
		b := make([]byte, 7)
		b[0] = 0x1E
		binary.LittleEndian.PutUint16(b[1:3], stat)
		binary.LittleEndian.PutUint16(b[3:5], value)
		return b
	}
	u32 := func(stat uint16, value uint32) []byte {
		b := make([]byte, 9)
		b[0] = 0x1F
		binary.LittleEndian.PutUint16(b[1:3], stat)
		binary.LittleEndian.PutUint32(b[5:9], value)
		return b
	}
	var out []byte
	out = append(out, u16(statStrength, row[0])...)
	out = append(out, u16(statDexterity, row[1])...)
	out = append(out, u16(statVitality, row[2])...)
	out = append(out, u16(statEnergy, row[3])...)
	out = append(out, u16(statLevel, 1)...)
	out = append(out, u32(statMaxLife, uint32(row[4])<<8)...)
	out = append(out, u32(statLife, uint32(row[4])<<8)...)
	out = append(out, u32(statMaxMana, uint32(row[5])<<8)...)
	out = append(out, u32(statMana, uint32(row[5])<<8)...)
	out = append(out, u32(statMaxStamina, uint32(row[6])<<8)...)
	out = append(out, u32(statStamina, uint32(row[6])<<8)...)
	return out
}

// pong answers a 13-byte 6D ping (or stands in for one, unrequested pongs are
// harmless): 8F + eight dwords, the tick echoed, the rest zero.
func pong(ping []byte) []byte {
	p := make([]byte, 33)
	p[0] = 0x8F
	if len(ping) >= 5 {
		copy(p[1:5], ping[1:5])
	}
	return p
}

func trimNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
