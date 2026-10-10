package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"openpak/battlenet/redact"
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
	remote := redact.Tag(conn.RemoteAddr().String())
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
	// online path), so the player is the oldest GameJoin still waiting for its
	// dial (takePendingJoin). The connection holds that account's seat in the
	// game until it closes.
	join := takePendingJoin()
	if join.gameID != 0 {
		seatGame(join.gameID, join.subject)
		defer leaveGame(join.gameID, join.subject)
	}
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
	r := bufio.NewReader(conn)
	for {
		// An idle limit, not a lifetime: the client pings every 5 s, and the
		// game (its lobby entry included) lives as long as this connection.
		// A fixed 2-minute deadline used to end every game at the 2-minute mark.
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
		id, err := r.ReadByte()
		if err != nil {
			log.Printf("D2GS %s closed: %v", remote, err)
			return
		}
		hdr := make([]byte, 0x19)
		hdr[0] = id
		total, need, ok := clientPacketLen(id, hdr)
		if ok && need > 1 {
			if _, err := io.ReadFull(r, hdr[1:need]); err != nil {
				log.Printf("D2GS %s: short header %#x: %v", remote, id, err)
				return
			}
			total, need, ok = clientPacketLen(id, hdr)
		}
		if !ok {
			// Unknown id: log it with what follows and drop the buffered bytes
			// rather than the connection; the stream resyncs at the next read.
			rest, _ := r.Peek(r.Buffered())
			log.Printf("D2GS %s: unknown packet %#x, %d bytes buffered after it", remote, id, len(rest))
			if traceBodies {
				log.Printf("D2GS %s: unknown packet rest: %x", remote, rest)
			}
			r.Discard(len(rest))
			continue
		}
		pkt := make([]byte, total)
		copy(pkt, hdr[:need])
		if _, err := io.ReadFull(r, pkt[need:]); err != nil {
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
				if _, err := conn.Write(append(joinBurst(join.class, join.name, flags), assignNPCs()...)); err != nil {
					return
				}
				log.Printf("D2GS %s: join burst sent (class %d, edition %d, game %d, seed %#x, area %d)", remote, join.class, join.edition, join.gameID, townSeed, townArea)
			}
		case 0x68:
			log.Printf("D2GS %s: logon packet (unexpected on this build): class=%d", remote, pkt[7])
		case 0x6B:
			log.Printf("D2GS %s: client reports loaded", remote)
		case 0x69:
			log.Printf("D2GS %s: client left the game", remote)
		case 0x2F: // reached an NPC: open its dialog
			npc := binary.LittleEndian.Uint32(pkt[1:5])
			log.Printf("D2GS %s: talk to npc %d", remote, npc)
			if _, err := conn.Write(npcDialog(npc)); err != nil {
				return
			}
		case 0x30:
			log.Printf("D2GS %s: closed dialog with npc %d", remote, binary.LittleEndian.Uint32(pkt[1:5]))
		case 0x42, 0x6F, 0x01, 0x03:
		default:
			if traceBodies {
				log.Printf("D2GS %s: client packet %#x %x", remote, id, pkt[1:])
			} else {
				log.Printf("D2GS %s: client packet %#x (%d bytes)", remote, id, len(pkt)-1)
			}
		}
	}
}

// clientPacketSizes: lengths including the id byte, for the UPDATE build
// (private scratch, traced from the send sites).
// Variable-length ids are in clientPacketLen.
var clientPacketSizes = map[byte]int{
	0x01: 210, 0x02: 214, 0x03: 210, 0x04: 214, 0x05: 5, 0x06: 9, 0x07: 9, 0x08: 210, 0x09: 214,
	0x0A: 214, 0x0B: 5, 0x0C: 5, 0x0D: 9, 0x0E: 9, 0x0F: 210, 0x10: 214, 0x11: 214, 0x12: 9,
	0x13: 9, 0x16: 17, 0x17: 5, 0x18: 17, 0x19: 17, 0x1A: 9, 0x1B: 13, 0x1C: 9, 0x1D: 13, 0x1E: 21,
	0x21: 21, 0x24: 24, 0x25: 13, 0x26: 36, 0x27: 17, 0x28: 21, 0x29: 21, 0x2A: 21, 0x2D: 9,
	0x2E: 21, 0x2F: 5, 0x30: 5, 0x31: 8, 0x32: 24, 0x33: 24, 0x34: 21, 0x35: 16, 0x36: 17,
	0x37: 9, 0x38: 9, 0x39: 5, 0x3A: 5, 0x3B: 5, 0x3C: 9, 0x3D: 3, 0x3E: 1, 0x3F: 1, 0x40: 5,
	0x41: 13, 0x42: 7, 0x43: 9, 0x44: 5, 0x47: 9, 0x49: 1, 0x4A: 1, 0x4B: 5, 0x4C: 3, 0x4D: 5,
	0x4F: 6, 0x50: 30, 0x51: 17, 0x52: 13, 0x53: 21, 0x54: 21, 0x55: 29, 0x56: 21, 0x57: 29,
	0x58: 21, 0x5A: 37, 0x5B: 25, 0x5C: 17, 0x5D: 98, 0x5E: 33, 0x5F: 25, 0x63: 25, 0x65: 13,
	0x68: 45, 0x69: 1, 0x6B: 1, 0x6D: 13, 0x6F: 5,
}

// clientPacketLen returns the total length of a client packet given its id and
// the bytes already read after it (headerNeeded bytes), for the ids whose
// length depends on a count in the header. ok=false means unknown id.
func clientPacketLen(id byte, hdr []byte) (total, headerNeeded int, ok bool) {
	switch id {
	case 0x20:
		return 5*int(hdr[7]) + 13, 8, true
	case 0x22:
		return 7*int(hdr[9]) + 17, 10, true
	case 0x2B, 0x2C:
		return int(binary.LittleEndian.Uint16(hdr[1:3])) + 3, 3, true
	case 0x4E:
		return 5*int(hdr[6]) + 12, 7, true
	case 0x60:
		return 7*int(hdr[0x18]) + 32, 0x19, true
	case 0x61:
		return int(hdr[2]) + 3, 3, true
	}
	if n, ok := clientPacketSizes[id]; ok {
		return n, 1, true
	}
	return 0, 0, false
}

// Act I townsfolk (monstats hcIdx from the public monster table: akara 148,
// gheed 147, kashya 150, charsi 154, warriv 155), placed around the spawn
// inside the revealed rooms. Unit ids follow the player's.
// ponytail: positions are offsets from the spawn, not the level's preset
// markers; good enough to talk to them, wrong for the map.
type townNPC struct {
	id     uint32
	code   uint16
	dx, dy int
}

var townNPCs = []townNPC{
	{2, 148, 30, -20},  // Akara
	{3, 150, -30, -20}, // Kashya
	{4, 154, 30, 20},   // Charsi
	{5, 147, -30, 20},  // Gheed
	{6, 155, 0, 40},    // Warriv
}

// assignNPCs: AC per NPC with a one-byte state stream: mode 1 (neutral) in
// bits 0-3, no component data. Without it the mode reads as 0, the death
// mode, and the client draws a corpse it will never target.
func assignNPCs() []byte {
	var out []byte
	for _, n := range townNPCs {
		p := make([]byte, 14)
		p[0] = 0xAC
		binary.LittleEndian.PutUint32(p[1:5], n.id)
		binary.LittleEndian.PutUint16(p[5:7], n.code)
		binary.LittleEndian.PutUint16(p[7:9], uint16(spawnX+n.dx))
		binary.LittleEndian.PutUint16(p[9:11], uint16(spawnY+n.dy))
		p[11] = 100 // life %
		p[12] = 0x0e
		p[13] = 0x01 // mode 1 = neutral
		out = append(out, p...)
	}
	return out
}

// npcDialog opens the NPC dialog panel. 27 (40 bytes: 01, u32 npcId, u8, u8
// message count, 32 bytes) creates the NPC's message list, which the 28
// handler (01, u32 npcId, u8, 96-byte quest blob) requires: without a fresh 27
// before every 28 the panel opens empty with no close path and the game sits
// stuck. Talk and Trade entries come from the client's own tables, so an
// all-zero blob is a valid "no quests yet".
func npcDialog(npcID uint32) []byte {
	msgs := make([]byte, 40)
	msgs[0] = 0x27
	msgs[1] = 1
	binary.LittleEndian.PutUint32(msgs[2:6], npcID)
	p := make([]byte, 103)
	p[0] = 0x28
	p[1] = 1
	binary.LittleEndian.PutUint32(p[2:6], npcID)
	return append(msgs, p...)
}

type pendingJoin struct {
	name    string
	class   byte
	edition byte // 1 classic, 2 expansion, 3 RotW: the character's, per the roster flags
	gameID  uint32
	subject string // the account that made the GameJoin; "" for the fallback
	at      time.Time
}

// The update build never identifies itself on the game socket, and every
// connection arrives from Traefik's address, so a dialling client can only be
// matched to a GameJoin by order. Until v0.2.0 that was one slot for the whole
// box: two GameJoins before the first dial handed both clients the second
// player's character. Now each GameJoin queues, the oldest live one is taken
// first, and entries older than pendingJoinTTL (a GameJoin whose client never
// dialled) are dropped.
//
// ponytail: FIFO is the best ordering available without an identity on the
// socket. Two clients dialling in the opposite order from their GameJoins
// (milliseconds apart) still swap; key it by the logon token if a build ever
// sends 0x68 online.
const pendingJoinTTL = 30 * time.Second

var (
	pendingMu    sync.Mutex
	pendingJoins []pendingJoin
)

// rememberPendingJoin notes who is about to dial the game host, from the
// joining account's own roster entry the GameJoin named. An ID that is not on
// that account's roster gets the fallback name, never another account's
// character.
func rememberPendingJoin(subject string, characterID uint64, gameID uint32) {
	j := pendingJoin{name: "Player", edition: 1, gameID: gameID, subject: subject, at: time.Now()}
	if c, ok := findCharacter(subject, characterID); ok {
		j.name, j.class = c.Name, byte(c.Class)
		if c.Flags&0x20 != 0 {
			j.edition = 2
		}
	}
	pendingMu.Lock()
	pendingJoins = append(pendingJoins, j)
	pendingMu.Unlock()
}

func takePendingJoin() pendingJoin {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	cutoff := time.Now().Add(-pendingJoinTTL)
	for len(pendingJoins) > 0 {
		j := pendingJoins[0]
		pendingJoins = pendingJoins[1:]
		if j.at.After(cutoff) {
			return j
		}
	}
	return pendingJoin{name: "Player", edition: 1}
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
