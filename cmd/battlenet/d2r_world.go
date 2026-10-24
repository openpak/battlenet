package main

import (
	"encoding/binary"
	"log"
	"net"
	"sync"
)

// The shared part of a game: every connection seated in the same GameJoin
// game sees the other players in town and their movement. Town only: no
// monsters, items or combat yet (docs/d2r-protocol.md, "Game server
// milestones").
//
// Wire facts (update build, client->server table in docs/d2r-protocol.md):
//   - the local player is never position-corrected by 0F/24, so movement is
//     relayed to the other clients only, never echoed to the mover;
//   - c2s 42 (analog stick, once per frame) is relayed as s2c 24
//     `24 | u8 type | u32 id | u16 dir | u8 0 | u16 x | u16 y` (13 bytes);
//   - c2s 01/03 (walk/run to a point, 210 bytes) is relayed as s2c 0F
//     `0F | u8 type | u32 id | u8 mode | u16 x | u16 y | u8 0 | u16 curX | u16 curY` (16 bytes);
//   - a remote player appears with the same 59 AssignPlayer the local one
//     gets (it creates a type-0 unit; no 0B for it) and goes with 0A
//     `0A | u8 type | u32 id` (6 bytes).

const maxPlayersPerGame = 8

// A unit placed outside an active room makes the client exit ("Unable to find
// room for unit"). Players can only walk inside the rooms the burst revealed,
// so this box (the 3x3 reveal cells, in subtiles) only guards against a bad
// report.
const (
	townMinX = 0x460 * 5
	townMaxX = (0x470 + 8) * 5
	townMinY = 0x448 * 5
	townMaxY = (0x458 + 8) * 5
)

type gsPlayer struct {
	id    uint32
	class byte
	name  string
	x, y  uint16
	ready bool // its own join burst is queued: it may receive and be seen
	out   chan []byte
	conn  net.Conn
	world *gsWorld
}

type gsWorld struct {
	gameID  uint32
	players map[uint32]*gsPlayer
}

var (
	worldsMu sync.Mutex
	worlds   = map[uint32]*gsWorld{}
)

// joinWorld seats a new player in the game's world (a private one when the
// connection matched no GameJoin) and starts its writer. ok=false: the game
// is full.
func joinWorld(gameID uint32, class byte, name string, conn net.Conn) (*gsPlayer, bool) {
	worldsMu.Lock()
	defer worldsMu.Unlock()
	w := worlds[gameID]
	if gameID == 0 || w == nil {
		w = &gsWorld{gameID: gameID, players: map[uint32]*gsPlayer{}}
		if gameID != 0 {
			worlds[gameID] = w
		}
	}
	if len(w.players) >= maxPlayersPerGame {
		return nil, false
	}
	id := uint32(1)
	for w.players[id] != nil {
		id++
	}
	p := &gsPlayer{id: id, class: class, name: name, x: spawnX, y: spawnY, out: make(chan []byte, 512), conn: conn, world: w}
	w.players[id] = p
	go p.writer()
	return p, true
}

func (p *gsPlayer) writer() {
	for b := range p.out {
		if _, err := p.conn.Write(b); err != nil {
			p.conn.Close()
			for range p.out {
			}
			return
		}
	}
}

// send queues bytes for this player in order. A client too slow to drain 512
// queued packets is cut off rather than allowed to stall the game.
func (p *gsPlayer) send(b []byte) {
	select {
	case p.out <- b:
	default:
		log.Printf("D2GS player %d: send queue full, closing", p.id)
		p.conn.Close()
	}
}

// enterTown runs once the player's own join burst is queued: the others
// already in town are assigned to the newcomer, and the newcomer to them.
func (p *gsPlayer) enterTown() {
	worldsMu.Lock()
	defer worldsMu.Unlock()
	p.ready = true
	for _, o := range p.world.players {
		if o == p || !o.ready {
			continue
		}
		p.send(assignPlayer(o.id, o.class, o.name, o.x, o.y))
		o.send(assignPlayer(p.id, p.class, p.name, p.x, p.y))
	}
}

// leave removes the player and tells the others to drop its unit.
func (p *gsPlayer) leave() {
	worldsMu.Lock()
	defer worldsMu.Unlock()
	delete(p.world.players, p.id)
	if len(p.world.players) == 0 && worlds[p.world.gameID] == p.world {
		delete(worlds, p.world.gameID)
	}
	for _, o := range p.world.players {
		if !p.ready || !o.ready {
			continue
		}
		o.send([]byte{0x0A, 0x00, byte(p.id), byte(p.id >> 8), byte(p.id >> 16), byte(p.id >> 24)})
	}
	close(p.out)
}

func (p *gsPlayer) broadcast(b []byte) {
	worldsMu.Lock()
	defer worldsMu.Unlock()
	for _, o := range p.world.players {
		if o != p && o.ready {
			o.send(b)
		}
	}
}

func inTown(x, y uint16) bool {
	return x >= townMinX && x < townMaxX && y >= townMinY && y < townMaxY
}

// analogMove handles c2s 42 `42 | u16 dir | u16 curX | u16 curY`.
func (p *gsPlayer) analogMove(pkt []byte) {
	dir := binary.LittleEndian.Uint16(pkt[1:3])
	x, y := binary.LittleEndian.Uint16(pkt[3:5]), binary.LittleEndian.Uint16(pkt[5:7])
	if !inTown(x, y) {
		return
	}
	p.setPos(x, y)
	out := make([]byte, 13)
	out[0] = 0x24
	binary.LittleEndian.PutUint32(out[2:6], p.id)
	binary.LittleEndian.PutUint16(out[6:8], dir)
	binary.LittleEndian.PutUint16(out[9:11], x)
	binary.LittleEndian.PutUint16(out[11:13], y)
	p.broadcast(out)
}

// walkTo handles c2s 01 (walk) / 03 (run): target @1, current position @5.
func (p *gsPlayer) walkTo(pkt []byte) {
	tx, ty := binary.LittleEndian.Uint16(pkt[1:3]), binary.LittleEndian.Uint16(pkt[3:5])
	cx, cy := binary.LittleEndian.Uint16(pkt[5:7]), binary.LittleEndian.Uint16(pkt[7:9])
	if !inTown(tx, ty) || !inTown(cx, cy) {
		return
	}
	p.setPos(tx, ty)
	mode := byte(0x01) // walk
	if pkt[0] == 0x03 {
		mode = 0x17 // run
	}
	out := make([]byte, 16)
	out[0] = 0x0F
	binary.LittleEndian.PutUint32(out[2:6], p.id)
	out[6] = mode
	binary.LittleEndian.PutUint16(out[7:9], tx)
	binary.LittleEndian.PutUint16(out[9:11], ty)
	binary.LittleEndian.PutUint16(out[12:14], cx)
	binary.LittleEndian.PutUint16(out[14:16], cy)
	p.broadcast(out)
}

func (p *gsPlayer) setPos(x, y uint16) {
	worldsMu.Lock()
	p.x, p.y = x, y
	worldsMu.Unlock()
}

// assignPlayer is s2c 59 (71 bytes on the update build): u32 id, u8 class,
// char name[61], u16 x, u16 y in subtiles.
func assignPlayer(id uint32, class byte, name string, x, y uint16) []byte {
	b := make([]byte, 71)
	b[0] = 0x59
	binary.LittleEndian.PutUint32(b[1:5], id)
	b[5] = class
	copy(b[6:66], name)
	binary.LittleEndian.PutUint16(b[67:69], x)
	binary.LittleEndian.PutUint16(b[69:71], y)
	return b
}
