package main

import (
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
// ponytail: handshake only, logs everything, no world. The next step is the
// in-game packet stream, which needs a D2 game simulation behind this socket.

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

// serveGame walks one connection through the handshake and logs every packet
// after it. It returns the last client packet seen so the test can check the
// order without a real title.
func serveGame(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("D2GS %s connected", remote)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))

	if _, err := conn.Write([]byte{0xAF, 0x00}); err != nil {
		return
	}
	logon := make([]byte, 45)
	if _, err := io.ReadFull(conn, logon); err != nil || logon[0] != 0x68 {
		log.Printf("D2GS %s: no logon packet (%x, %v)", remote, logon, err)
		return
	}
	hash := binary.LittleEndian.Uint32(logon[1:5])
	token := binary.LittleEndian.Uint16(logon[5:7])
	charID := binary.LittleEndian.Uint64(logon[21:29])
	name := string(trimNUL(logon[29:45]))
	log.Printf("D2GS %s: logon hash=%d token=%d class=%d character=%d name=%q", remote, hash, token, logon[7], charID, name)

	flags := make([]byte, 8)
	flags[0] = 0x01
	flags[1] = 0 // Normal
	binary.LittleEndian.PutUint32(flags[2:6], d2gsFlagBase|d2gsFlagExpansion)
	flags[6] = 1 // expansion
	flags[7] = 0 // ladder
	if _, err := conn.Write(append(flags, 0x00)); err != nil {
		return
	}

	buf := make([]byte, 4096)
	sentStart := false
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			log.Printf("D2GS %s RX %x", remote, buf[:n])
			if buf[0] == 0x6D && !sentStart {
				sentStart = true
				if _, err := conn.Write([]byte{0x02}); err != nil {
					return
				}
			}
			if buf[0] == 0x6D && n >= 5 {
				pong := make([]byte, 33)
				pong[0] = 0x8F
				copy(pong[1:5], buf[1:5])
				if _, err := conn.Write(pong); err != nil {
					return
				}
			}
		}
		if err != nil {
			log.Printf("D2GS %s closed: %v", remote, err)
			return
		}
	}
}

func trimNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
