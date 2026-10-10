package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"openpak/battlenet/redact"
)

const (
	gameCreate    = uint32(0x930fbb99)
	gameJoin      = uint32(0xa5cc1951)
	gameDetail    = uint32(0xb5cd1354)
	gameListAll   = uint32(0xa5ea9f6d)
	gameCreateCan = uint32(0x38274117)
)

// Game management (lobby records only).
//
// CORRECTION 2026-09-30 (docs/d2r-protocol.md, "CORRECTION 2026-09-30"): the
// title does NOT host games. After GameJoin it dials field 1 on TCP 443 with
// its own OpenSSL, trust pinned to a DigiCert bundle in the binary, and expects
// a Diablo II game server there (server speaks first). Field 2 (port) and
// field 6 are never read. This service keeps the lobby record and answers the
// RPCs correctly up to that hand-off; the game server itself does not exist yet.
// The "GameMaster / UDP 6112" wording below is the earlier, wrong model and is
// kept only because the env knobs carry those names.
//
// Direct-LAN limitation of this patch: GameJoin returns the creating
// session's observed IP (or BATTLENET_GAMEMASTER_HOST when that IP is not
// directly dialable, e.g. behind NAT or in a container) and UDP port 6112
// (or BATTLENET_GAMEMASTER_PORT). Both consoles must be able to reach that
// host:port over UDP. No game_proxy relay or NAT traversal is implemented
// here; peers that cannot punch through will not connect.
//
// Request layouts below are the pinned mappings from docs/d2r-protocol.md.
// GameCreate: {2 string key (lowercased name), 3 string password,
// 6 string unknown (empty), 9 varint level difference, 10 varint max players,
// 11 varint flags/bitfield, 12 string unknown, 13 string unknown,
// 14 varint difficulty, 15 varint unknown}.
// GameJoin: {1 varint character id, 2 string key, 3 string password,
// 4 string unknown (empty), 5-7 varint 0, 8 varint 1, 9 varint 0}.
// No other request field meanings are assumed.

// defaultGameUDPPort is the UDP port the title binds (Bind.Success|port=6112).
const defaultGameUDPPort = 6112

// gameCreateRequest carries only the documented GameCreateRequest fields.
type gameCreateRequest struct {
	Key        string
	Password   string
	Unknown6   string
	LevelDiff  uint64
	MaxPlayers uint64
	Flags      uint64
	Unknown12  string
	Unknown13  string
	Difficulty uint64
	Unknown15  uint64
}

// gameJoinRequest carries only the documented GameJoinRequest fields needed
// for the lobby lookup.
type gameJoinRequest struct {
	CharacterID uint64
	Key         string
	Password    string
}

// gameRecord is one lobby entry: the server-side convention pairing the
// GameCreate allocation with the later GameJoin answer. The client retains
// nothing from GameCreateResponse, so the ID lives here until GameJoin.
type gameRecord struct {
	ID          uint32
	Key         string // normalized lookup key (lowercased field 2)
	DisplayName string // field 2 as sent; no separate display field is documented
	Host        string // GameMaster textual host/IP for Join field 1
	Port        uint32 // UDP port for Join field 2
	Password    string // field 3, for Join validation
	CreatedAt   time.Time
	LevelDiff   uint64
	MaxPlayers  uint64
	Flags       uint64
	Difficulty  uint64
	Creator     string // creating session remote, for logs only

	// Owner is the creating account's battlenet subject; Version is the
	// creator's GameVersion.SetGameVersion string ("" when it never sent one).
	Owner   string
	Version string
	// Seated counts the game-server connections each account holds in this
	// game (seatGame/leaveGame). A game whose last seat leaves is closed and
	// removed; one never seated within staleGameAfter is dropped as abandoned.
	Seated     map[string]int
	EverSeated bool
	Players    int // seat count in a listedGames snapshot; unused on live records

	// CharFlags are the owner's character flags (hardcore 0x4, expansion
	// 0x20, ladder 0x40), stamped when the owner GameJoins its own game with
	// that character; CharKnown is false until then and the Game List's
	// hardcore/ladder/game-type filters pass the game.
	CharFlags uint64
	CharKnown bool
	// Desecrated is GameListInfo field 13. No GameCreate field is known to
	// carry it, so OpenPak games are never desecrated.
	Desecrated bool
}

// staleGameAfter drops a game nobody ever reached the game server for (the
// creator backed out between GameCreate and the dial, or never dialled).
const staleGameAfter = 2 * time.Minute

// liveLocked reports whether a record still stands for a game. Call with
// gameMu held.
func (rec *gameRecord) liveLocked(now time.Time) bool {
	if rec.EverSeated {
		return len(rec.Seated) > 0
	}
	return now.Sub(rec.CreatedAt) < staleGameAfter
}

// fullLocked reports whether every seat is taken. Max players 0 (never sent
// by the title) means no limit. Call with gameMu held.
func (rec *gameRecord) fullLocked() bool {
	return rec.MaxPlayers > 0 && uint64(len(rec.Seated)) >= rec.MaxPlayers
}

var (
	gameMu     sync.Mutex
	gamesByKey        = make(map[string]*gameRecord)
	gameNextID uint32 = 1
)

// normalizeGameKey lowercases the game key the way the client does for
// field 2, so create and join agree regardless of display casing.
func normalizeGameKey(name string) string {
	return strings.ToLower(name)
}

// parseGameCreate reads the documented GameCreateRequest fields.
func parseGameCreate(body []byte) gameCreateRequest {
	var req gameCreateRequest
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			break
		}
		body = body[n:]
		switch {
		case typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return req
			}
			body = body[m:]
			switch num {
			case 2:
				req.Key = string(v)
			case 3:
				req.Password = string(v)
			case 6:
				req.Unknown6 = string(v)
			case 12:
				req.Unknown12 = string(v)
			case 13:
				req.Unknown13 = string(v)
			}
		case typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return req
			}
			body = body[m:]
			switch num {
			case 9:
				req.LevelDiff = v
			case 10:
				req.MaxPlayers = v
			case 11:
				req.Flags = v
			case 14:
				req.Difficulty = v
			case 15:
				req.Unknown15 = v
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, body)
			if m < 0 {
				return req
			}
			body = body[m:]
		}
	}
	return req
}

// parseGameJoin reads the documented GameJoinRequest fields.
func parseGameJoin(body []byte) gameJoinRequest {
	var req gameJoinRequest
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			break
		}
		body = body[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return req
			}
			req.CharacterID = v
			body = body[m:]
		case typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return req
			}
			body = body[m:]
			switch num {
			case 2:
				req.Key = string(v)
			case 3:
				req.Password = string(v)
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, body)
			if m < 0 {
				return req
			}
			body = body[m:]
		}
	}
	return req
}

// gameMasterHost extracts the host/IP from a session remote "host:port"
// without ever returning the Battle.net TCP source port. It handles IPv4
// ("10.0.0.2:54321"), bracketed IPv6 ("[fd00::1]:54321"), and bare values
// without a port (returned as-is for tests and unix-style remotes).
func gameMasterHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

// gameMasterPort returns the UDP game port: BATTLENET_GAMEMASTER_PORT when set
// to a valid 1-65535 value, otherwise the title's bound port 6112.
func gameMasterPort() uint32 {
	if raw := strings.TrimSpace(os.Getenv("BATTLENET_GAMEMASTER_PORT")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 65535 {
			return uint32(v)
		}
		log.Printf("GAME: ignoring invalid BATTLENET_GAMEMASTER_PORT=%q, using %d", raw, defaultGameUDPPort)
	}
	return defaultGameUDPPort
}

// gameMasterEndpoint resolves the Join field-1/field-2 endpoint: the
// BATTLENET_GAMEMASTER_HOST override when set (for NAT/container deployments
// where the observed session IP is not directly dialable), otherwise the
// creating session's IP with the TCP source port stripped.
// defaultGameHost is the name the title dials on TCP 443 after GameJoin; the
// certificate Traefik serves for it must chain to the CA in the trust patch.
const defaultGameHost = "d2gs.battle.net"

func gameMasterEndpoint(remote string) (string, uint32) {
	if host := strings.TrimSpace(os.Getenv("BATTLENET_GAMEMASTER_HOST")); host != "" {
		return host, gameMasterPort()
	}
	_ = gameMasterHost(remote) // the creator's address is logged, never dialled
	return env("BATTLENET_GAME_HOST", defaultGameHost), gameMasterPort()
}

// allocateGameIDLocked hands out a nonzero uint32 game ID. Call with gameMu held.
func allocateGameIDLocked() uint32 {
	for {
		id := gameNextID
		gameNextID++
		if gameNextID == 0 {
			gameNextID = 1
		}
		if id != 0 {
			return id
		}
	}
}

// createGameRecord allocates an ID, records the lobby entry for the creating
// account, and returns it. A name already held by another account's live game
// is refused (ok=false) instead of silently replacing that game, which would
// hand its seats and its GameJoins to whoever created second. The same account
// re-creating its own name replaces the old record. Callers must hold no locks;
// this takes gameMu.
func createGameRecord(remote, owner, version string, req gameCreateRequest) (*gameRecord, bool) {
	host, port := gameMasterEndpoint(remote)
	gameMu.Lock()
	defer gameMu.Unlock()
	key := normalizeGameKey(req.Key)
	now := time.Now().UTC()
	if old, ok := gamesByKey[key]; ok && old.Owner != owner && old.liveLocked(now) {
		return old, false
	}
	rec := &gameRecord{
		ID:          allocateGameIDLocked(),
		Key:         key,
		DisplayName: req.Key,
		Host:        host,
		Port:        port,
		Password:    req.Password,
		CreatedAt:   now,
		LevelDiff:   req.LevelDiff,
		MaxPlayers:  req.MaxPlayers,
		Flags:       req.Flags,
		Difficulty:  req.Difficulty,
		Creator:     remote,
		Owner:       owner,
		Version:     version,
		Seated:      map[string]int{},
	}
	gamesByKey[rec.Key] = rec
	return rec, true
}

// lookupGame finds a live lobby entry by normalized key/name.
func lookupGame(name string) (*gameRecord, bool) {
	gameMu.Lock()
	defer gameMu.Unlock()
	rec, ok := gamesByKey[normalizeGameKey(name)]
	if !ok || !rec.liveLocked(time.Now()) {
		return nil, false
	}
	return rec, true
}

// gameIsFull reports whether a game has no free seat for this account (an
// account already seated keeps its seat on a rejoin).
func gameIsFull(rec *gameRecord, subject string) bool {
	gameMu.Lock()
	defer gameMu.Unlock()
	if _, seated := rec.Seated[subject]; seated {
		return false
	}
	return rec.fullLocked()
}

// seatGame records that an account's game-server connection for gameID is up.
func seatGame(gameID uint32, subject string) {
	gameMu.Lock()
	defer gameMu.Unlock()
	for _, rec := range gamesByKey {
		if rec.ID == gameID {
			rec.Seated[subject]++
			rec.EverSeated = true
			return
		}
	}
}

// leaveGame releases one game-server connection's seat. When the last seat
// leaves, the game is closed: removed from the lobby, no longer listed or
// joinable.
func leaveGame(gameID uint32, subject string) {
	gameMu.Lock()
	defer gameMu.Unlock()
	for key, rec := range gamesByKey {
		if rec.ID != gameID {
			continue
		}
		if rec.Seated[subject] > 1 {
			rec.Seated[subject]--
		} else {
			delete(rec.Seated, subject)
		}
		if len(rec.Seated) == 0 {
			delete(gamesByKey, key)
			log.Printf("GAME %d closed: last player left", gameID)
		}
		return
	}
}

// listedGames is what a Game List shows a viewer: live games with a free
// seat, no password, and the viewer's game version (when both sides sent
// one). Password-protected games are joined by name and never listed: the
// title's quick "private game" fills the password with the game name
// (docs/d2r-protocol.md, "GameCreateRequest / GameJoinRequest, pinned"), so a
// listed password game would publish exactly the games meant to be private.
// Oldest first.
func listedGames(viewerVersion string) []gameRecord {
	gameMu.Lock()
	defer gameMu.Unlock()
	now := time.Now()
	var out []gameRecord
	for key, rec := range gamesByKey {
		if !rec.liveLocked(now) {
			delete(gamesByKey, key)
			continue
		}
		if rec.Password != "" || rec.fullLocked() {
			continue
		}
		if viewerVersion != "" && rec.Version != "" && viewerVersion != rec.Version {
			continue
		}
		cp := *rec
		cp.Seated = nil
		cp.Players = len(rec.Seated)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// checkGamePassword validates a Join against the stored password. An empty
// stored password means an open game and accepts anything; otherwise the
// join password must match exactly.
func checkGamePassword(rec *gameRecord, password string) bool {
	if rec.Password == "" {
		return true
	}
	return password == rec.Password
}

// resetGameRegistry clears the lobby and ID counter. Tests only.
func resetGameRegistry() {
	gameMu.Lock()
	defer gameMu.Unlock()
	gamesByKey = make(map[string]*gameRecord)
	gameNextID = 1
}

// gameCreateBody encodes GameCreateResponse: only field 5 is read by the RPC
// adapter (and the CreatingGame callback ignores even that), so the ID here
// is a server-side convention, not a value the client carries into Join.
func gameCreateBody(gameID uint32) []byte {
	return pbVarint(5, uint64(gameID))
}

// gameJoinBody encodes the direct-LAN GameJoinResponse: field 1 is the
// GameMaster textual host/IP, field 2 is the UDP port (narrowed to uint16 by
// the client), and field 4 is the allocated game number/ID used by the
// transport handshake. Field 3 is omitted (accepted and stored by the client
// but with no reader on this build; never present it as an active token) and
// field 6 is omitted so the client takes the immediate-connection path
// rather than the queued/pending path.
func gameJoinBody(host string, gameID uint32) []byte {
	// 1 = the host the title dials on TCP 443 (its TLS hostname check runs on
	// it); 3 = game hash, 4 = token: both echoed in the client's logon packet,
	// 4 must be non-zero. Field 2 is never read by the title; field 6 (queued)
	// must stay absent.
	out := pbBytes(1, []byte(host))
	out = append(out, pbVarint(3, uint64(gameID))...)
	return append(out, pbVarint(4, uint64(gameID))...)
}

// tunedGameBody decodes a raw-hex research override. It reports whether the
// key was present at all so the caller can fall through to the dynamic path
// when no override exists.
func tunedGameBody(raw, what, remote string) ([]byte, bool, bool) {
	if raw == "" {
		return nil, false, false
	}
	body, err := hex.DecodeString(raw)
	if err != nil {
		log.Printf("D2RSVC %s: %s hex is not decodable: %v", remote, what, err)
		return nil, true, false
	}
	log.Printf("D2RSVC %s: %s -> tuned body %d bytes (%s)", remote, what, len(body), raw)
	return body, true, true
}

// gameDetailName reads GameDetailRequest {1: string name}; the live client
// sends 0a0e followed by the 14-character game name.
func gameDetailName(body []byte) string {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return ""
		}
		body = body[n:]
		if num == 1 && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return ""
			}
			return string(v)
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			return ""
		}
		body = body[m:]
	}
	return ""
}

// GameDetailResponse, from the parser's dispatch at 0x16f8100..0x16f8260:
//
//	1 bytes  (+0x38)   2 bytes  (+0x40)   5 bytes  (+0x50)
//	3,4,6,7,8 uint32
//	9 repeated message (array +0x60, count +0x68, cap +0x70)
//
// The 2026-09-21 consumer trace identified field 4 as elapsed seconds and
// fields 6/7 as the level and allowed level difference used to display the
// join restriction range. Field 5 is not consumed by the browser path;
// fields 3 and 8 are copied but not loaded there. None is its queue-release
// gate. Keep the tunables for descriptive-field work, not readiness sweeps.
//
// So the body is tunable from a file on the persistent volume, re-read on every
// request. Editing it changes the next answer with no restart and no dropped
// session. ponytail: a few hundred bytes read per RPC at one request every 3 s
// -- when this stops being a probe, fold the settled values into code and drop
// the file.
type gameDetailTune struct {
	Field1  *string `json:"field1"` // nil = echo the requested game name
	Field2  string  `json:"field2"`
	Field5  string  `json:"field5"`
	Field3  uint64  `json:"field3"`
	Field4  uint64  `json:"field4"` // elapsed game time in seconds
	Field6  uint64  `json:"field6"` // level used as the restriction-range center
	Field7  uint64  `json:"field7"` // allowed level difference; >=99 means any
	Field8  uint64  `json:"field8"`
	Players int     `json:"players"` // how many empty GameDetailPlayer entries in field 9

	// GameCreateResponse and GameJoinResponse bodies, as raw hex. They are
	// one-shot replies -- the client gets each exactly once and only then
	// starts polling GameDetail -- so they are the likelier gate, and they
	// cannot be probed by repetition the way GameDetail can. Hex rather than
	// named fields because their meanings are not settled. GameJoinResponse's
	// wire layout is settled; notably field 6 is a bool, while the other
	// varints are uint32 values (field 2 is narrowed to 16 bits by the result
	// adapter). The non-queued consumer passes fields 1/2 to
	// GameConnectionObject as its host/port and fields 3/4 as two uint32
	// transport arguments; field 6 is the queued/pending boolean. The concrete
	// GameMaster stores both words, but only field 4 has readers in that class;
	// field 3 is currently an inert stored value on this Switch build.
	CreateHex string `json:"create_hex"`
	JoinHex   string `json:"join_hex"`
}

func loadGameDetailTune() gameDetailTune {
	var t gameDetailTune
	path := env("BATTLENET_GAMEDETAIL_TUNE", "/var/lib/battlenet/gamedetail.json")
	blob, err := os.ReadFile(path)
	if err != nil {
		return t
	}
	if err := json.Unmarshal(blob, &t); err != nil {
		log.Printf("GAMEDETAIL: parsing %s: %v", path, err)
		return gameDetailTune{}
	}
	return t
}

func gameDetailBody(name string) ([]byte, gameDetailTune) {
	t := loadGameDetailTune()

	first := name
	if t.Field1 != nil {
		first = *t.Field1
	}

	out := pbBytes(1, []byte(first))
	if t.Field2 != "" {
		out = append(out, pbBytes(2, []byte(t.Field2))...)
	}
	if t.Field3 != 0 {
		out = append(out, pbVarint(3, t.Field3)...)
	}
	if t.Field4 != 0 {
		out = append(out, pbVarint(4, t.Field4)...)
	}
	if t.Field5 != "" {
		out = append(out, pbBytes(5, []byte(t.Field5))...)
	}
	if t.Field6 != 0 {
		out = append(out, pbVarint(6, t.Field6)...)
	}
	if t.Field7 != 0 {
		out = append(out, pbVarint(7, t.Field7)...)
	}
	if t.Field8 != 0 {
		out = append(out, pbVarint(8, t.Field8)...)
	}
	for i := 0; i < t.Players; i++ {
		out = append(out, pbBytes(9, nil)...)
	}
	return out, t
}

// handleGameRPC answers GameManagement. Returns false for methods that are not
// ours, so the caller falls back to the empty-body answer.
func handleGameRPC(s *gatewaySession, h d2rHeader) ([]byte, bool) {
	switch h.method {
	case gameDetail:
		name := gameDetailName(h.body)
		body, t := gameDetailBody(name)
		log.Printf("D2RSVC %s: GameDetail %s -> %d bytes  f3=%d f4=%d f6=%d f7=%d f8=%d players=%d",
			s.remote, redact.Tag(name), len(body), t.Field3, t.Field4, t.Field6, t.Field7, t.Field8, t.Players)
		return body, true
	case gameCreate:
		t := loadGameDetailTune()
		if body, present, ok := tunedGameBody(t.CreateHex, "GameCreate", s.remote); present {
			if ok {
				return body, true
			}
		}
		req := parseGameCreate(h.body)
		if s.subject == "" {
			log.Printf("D2RSVC %s: GameCreate refused: session has no account", s.remote)
			return nil, false
		}
		rec, ok := createGameRecord(s.remote, s.subject, s.gameVersion, req)
		if !ok {
			log.Printf("D2RSVC %s: GameCreate %s refused: another account's game has that name", s.remote, redact.Tag(req.Key))
			return nil, false
		}
		body := gameCreateBody(rec.ID)
		log.Printf("D2RSVC %s: GameCreate %s -> id=%d host=%s port=%d max=%d private=%v (%d-byte body)",
			s.remote, redact.Tag(req.Key), rec.ID, rec.Host, rec.Port, rec.MaxPlayers, rec.Password != "", len(body))
		return body, true
	case gameJoin:
		t := loadGameDetailTune()
		if body, present, ok := tunedGameBody(t.JoinHex, "GameJoin", s.remote); present {
			if ok {
				return body, true
			}
		}
		req := parseGameJoin(h.body)
		if s.subject == "" {
			log.Printf("D2RSVC %s: GameJoin refused: session has no account", s.remote)
			return nil, false
		}
		rec, ok := lookupGame(req.Key)
		if !ok {
			log.Printf("D2RSVC %s: GameJoin %s -> unknown game", s.remote, redact.Tag(req.Key))
			return nil, false
		}
		if !checkGamePassword(rec, req.Password) {
			log.Printf("D2RSVC %s: GameJoin %s -> password mismatch", s.remote, redact.Tag(req.Key))
			return nil, false
		}
		if gameIsFull(rec, s.subject) {
			log.Printf("D2RSVC %s: GameJoin %s -> full (%d players)", s.remote, redact.Tag(req.Key), rec.MaxPlayers)
			return nil, false
		}
		body := gameJoinBody(rec.Host, rec.ID)
		rememberPendingJoin(s.subject, req.CharacterID, rec.ID)
		if c, ok := findCharacter(s.subject, req.CharacterID); ok {
			stampGameCharacter(rec, s.subject, c.Flags)
		}
		log.Printf("D2RSVC %s: GameJoin %s -> host=%s port=%d id=%d (%d-byte body)",
			s.remote, redact.Tag(req.Key), rec.Host, rec.Port, rec.ID, len(body))
		return body, true
	case gameListAll:
		f := parseGameListRequest(h.body)
		games := filterGameList(listedGames(s.gameVersion), f)
		body := gameListBody(games, f.site())
		log.Printf("D2RSVC %s: GameList request %s -> %d game(s), %d-byte body",
			s.remote, f, len(games), len(body))
		return body, true
	}
	return nil, false
}

// GameList (GameManagement 0xa5ea9f6d), recovered 2026-10-10 from the live
// update build's list screen (decompiles in
// private scratch: the request builder at 0x817aec/0x816900
// and the row builder at 0xb3f534). CORRECTION to the first reading of the
// message classes: the client reads ONLY GameListResponse field 3, so a list
// sent as field 1 is dropped and the screen says "No games found".
//
//	GameListResponse: 3 repeated GameListInfo.
//	GameListInfo:     2 uint32 current players, 3 uint32 max players (a row
//	  with max <= current, or max 0, is hidden), 6 string game name (shown,
//	  and the key GameDetail/GameJoin carry), 8 string site (looked up in the
//	  request's field-9 latencies for the ping icon; no match = best bucket,
//	  not hidden), 9 uint32 difficulty 0-2, 13 bool desecrated. 1, 4, 7, 10,
//	  11 and 12 only feed a change hash; 5 is ignored by this build.
//	GameListRequest:  3 string name search, 4 uint32 the viewer's character
//	  flags (hardcore 0x4, ladder 0x40), 6 string preferred site, 7 and 10
//	  uint32 difficulty bitmasks (bit 0 normal, 1 nightmare, 2 hell; the
//	  builder ORs them from the character's progression and the ticked
//	  boxes), 9 repeated GameSiteLatency {1 string site, 2 uint32 latency},
//	  11 bool desecrated filter (absent: all; true: only desecrated; false:
//	  none desecrated), 12 uint32 game type (1 classic, 2 expansion).
//
// Client-side the list also drops names with 7+ ASCII digits unless they
// start with '_', rows over its ping filter, the local hidden list and rows
// past 40; the server caps at 40 too so the newest are not the ones lost.
const gameListMax = 40

// gameListFilter is a parsed GameListRequest.
type gameListFilter struct {
	Name       string
	CharFlags  uint64
	HasFlags   bool
	Site       string
	Diff7      uint64
	Has7       bool
	Diff10     uint64
	Has10      bool
	Sites      []string
	Desecrated *bool
	GameType   uint64
}

// site is the one GameListInfo field 8 carries: the viewer's preferred site,
// else the first one it measured. Echoing the viewer's own site is honest
// (every OpenPak game runs on one box) and lands in its best ping bucket.
func (f gameListFilter) site() string {
	if f.Site != "" {
		return f.Site
	}
	if len(f.Sites) > 0 {
		return f.Sites[0]
	}
	return ""
}

// difficultyMask is the set of difficulties the viewer accepts. Zero (no
// field, or masks that share no bit) filters nothing: a misread request must
// not empty the list.
func (f gameListFilter) difficultyMask() uint64 {
	mask := uint64(7)
	if f.Has7 {
		mask &= f.Diff7
	}
	if f.Has10 {
		mask &= f.Diff10
	}
	if mask == 0 {
		return 7
	}
	return mask
}

// String is the log form: no search text, only its length.
func (f gameListFilter) String() string {
	d := "-"
	if f.Desecrated != nil {
		d = strconv.FormatBool(*f.Desecrated)
	}
	return "search=" + strconv.Itoa(len(f.Name)) + "ch charflags=" + strconv.FormatUint(f.CharFlags, 16) +
		" diff7=" + strconv.FormatUint(f.Diff7, 10) + " diff10=" + strconv.FormatUint(f.Diff10, 10) +
		" desecrated=" + d + " type=" + strconv.FormatUint(f.GameType, 10) + " sites=" + strconv.Itoa(len(f.Sites))
}

func parseGameListRequest(body []byte) gameListFilter {
	var f gameListFilter
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return f
		}
		body = body[n:]
		if typ == protowire.VarintType {
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return f
			}
			body = body[m:]
			switch num {
			case 4:
				f.CharFlags, f.HasFlags = v, true
			case 7:
				f.Diff7, f.Has7 = v, true
			case 10:
				f.Diff10, f.Has10 = v, true
			case 11:
				b := v != 0
				f.Desecrated = &b
			case 12:
				f.GameType = v
			}
			continue
		}
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return f
			}
			body = body[m:]
			switch num {
			case 3:
				f.Name = string(v)
			case 6:
				f.Site = string(v)
			case 9:
				if site := gameSiteLatencySite(v); site != "" {
					f.Sites = append(f.Sites, site)
				}
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			return f
		}
		body = body[m:]
	}
	return f
}

// gameSiteLatencySite reads GameSiteLatency field 1.
func gameSiteLatencySite(b []byte) string {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return ""
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(b)
			return string(v)
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return ""
		}
		b = b[m:]
	}
	return ""
}

// filterGameList applies the viewer's request filters to listedGames'
// snapshot (which already dropped full, password and other-version games).
func filterGameList(games []gameRecord, f gameListFilter) []gameRecord {
	mask := f.difficultyMask()
	search := strings.ToLower(f.Name)
	var out []gameRecord
	for _, g := range games {
		if g.Difficulty > 2 || mask&(1<<g.Difficulty) == 0 {
			continue
		}
		if f.Desecrated != nil && g.Desecrated != *f.Desecrated {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(g.DisplayName), search) {
			continue
		}
		if g.CharKnown {
			if f.HasFlags && (g.CharFlags&0x44) != (f.CharFlags&0x44) {
				continue // hardcore and ladder must match the viewer's character
			}
			if expansion := g.CharFlags&0x20 != 0; (f.GameType == 1 && expansion) || (f.GameType == 2 && !expansion) {
				continue
			}
		}
		out = append(out, g)
		if len(out) == gameListMax {
			break
		}
	}
	return out
}

// stampGameCharacter records the owner's character flags on its game.
func stampGameCharacter(rec *gameRecord, subject string, flags uint64) {
	gameMu.Lock()
	defer gameMu.Unlock()
	if rec.Owner == subject {
		rec.CharFlags, rec.CharKnown = flags, true
	}
}

// gameListBody encodes GameListResponse for the listed games.
func gameListBody(games []gameRecord, site string) []byte {
	var out []byte
	for _, g := range games {
		players := uint64(g.Players)
		if players == 0 {
			players = 1 // created, the creator still on its way to the game server
		}
		maxPlayers := g.MaxPlayers
		if maxPlayers == 0 {
			maxPlayers = 8 // 0 hides the row; the title's own default is 8
		}
		info := pbVarint(2, players)
		info = append(info, pbVarint(3, maxPlayers)...)
		info = append(info, pbBytes(6, []byte(g.DisplayName))...)
		if site != "" {
			info = append(info, pbBytes(8, []byte(site))...)
		}
		info = append(info, pbVarint(9, g.Difficulty)...)
		if g.Desecrated {
			info = append(info, pbVarint(13, 1)...)
		}
		out = append(out, pbBytes(3, info)...)
	}
	return out
}
