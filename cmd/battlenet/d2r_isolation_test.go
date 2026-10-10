package main

import (
	"encoding/binary"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// d2rRequest frames one D2R-services request the way the title writes it.
func d2rRequest(service, method, token uint32, body []byte) []byte {
	hdr := pbVarint(1, uint64(service))
	hdr = append(hdr, pbVarint(2, uint64(method))...)
	hdr = append(hdr, pbVarint(3, uint64(token))...)
	hdr = append(hdr, pbVarint(5, uint64(len(body)))...)
	packet := make([]byte, 2)
	binary.BigEndian.PutUint16(packet, uint16(len(hdr)))
	return append(append(packet, hdr...), body...)
}

// d2rCall sends one request through the D2R-services handler and returns the
// response body (the first frame queued; pushes after it are dropped).
func d2rCall(t *testing.T, s *gatewaySession, service, method uint32, body []byte) []byte {
	t.Helper()
	if !handleD2RServices(s, d2rRequest(service, method, 1, body)) {
		t.Fatalf("request %#x/%#x not handled", service, method)
	}
	frames := drain(s)
	if len(frames) == 0 {
		t.Fatalf("request %#x/%#x: no response", service, method)
	}
	h, ok := parseD2RHeader(frames[0])
	if !ok {
		t.Fatalf("unparsable response %x", frames[0])
	}
	return h.body
}

// signIn opens a D2R-services session from the given address and proves the
// account with the token its LoginResponse carried.
func signIn(t *testing.T, remote, token string) *gatewaySession {
	t.Helper()
	s := newGatewaySession(remote)
	d2rCall(t, s, d2rServicesHash, d2rAuthSession, pbBytes(1, []byte(token)))
	return s
}

func rosterIDs(t *testing.T, body []byte) []uint64 {
	t.Helper()
	var ids []uint64
	for _, summary := range repeatedField(t, body, 2) {
		v, _ := decodeTestFields(t, summary)
		ids = append(ids, v[1])
	}
	return ids
}

// repeatedField returns every occurrence of a length-delimited field.
func repeatedField(t *testing.T, body []byte, field protowire.Number) [][]byte {
	t.Helper()
	var out [][]byte
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			t.Fatalf("bad tag in %x", body)
		}
		body = body[n:]
		if num == field && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				t.Fatalf("bad field %d", num)
			}
			out = append(out, v)
			body = body[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			t.Fatalf("bad field %d", num)
		}
		body = body[m:]
	}
	return out
}

// Two accounts behind one address (two emulators on one PC, or any two
// consoles behind Traefik) must never see each other's characters.
func TestTwoAccountsSameAddressDisjointRosters(t *testing.T) {
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	resetCharacters()

	const addr = "10.89.0.7:44312" // what Traefik looks like to us, for both
	a := signIn(t, addr, d2rSessionToken("1001"))
	b := signIn(t, addr, d2rSessionToken("1002"))
	if a.subject != "1001" || b.subject != "1002" {
		t.Fatalf("sessions did not prove their accounts")
	}

	create := func(s *gatewaySession, name string) uint64 {
		v, _ := decodeTestFields(t, d2rCall(t, s, characterHash, charCreate,
			append(append(pbBytes(1, []byte(name)), pbVarint(2, 4)...), pbVarint(3, 0)...)))
		return v[1]
	}
	idA := create(a, "Hosta")

	// B has no characters yet: an empty list, which sends the title to
	// character creation.
	if ids := rosterIDs(t, d2rCall(t, b, characterHash, charReadList, nil)); len(ids) != 0 {
		t.Fatalf("account B sees characters %v before creating any", ids)
	}
	idB := create(b, "Joina")

	gotA := rosterIDs(t, d2rCall(t, a, characterHash, charReadList, nil))
	gotB := rosterIDs(t, d2rCall(t, b, characterHash, charReadList, nil))
	if len(gotA) != 1 || gotA[0] != idA || len(gotB) != 1 || gotB[0] != idB || idA == idB {
		t.Fatalf("rosters not disjoint: A=%v (want [%d]) B=%v (want [%d])", gotA, idA, gotB, idB)
	}

	// B cannot delete A's character by naming its ID.
	d2rCall(t, b, characterHash, charDelete, pbVarint(1, idA))
	if got := rosterIDs(t, d2rCall(t, a, characterHash, charReadList, nil)); len(got) != 1 {
		t.Fatalf("account B deleted account A's character")
	}

	// The rosters survive a restart, still apart.
	resetCharacters()
	loadCharacters()
	if len(accountCharacters("1001")) != 1 || len(accountCharacters("1002")) != 1 {
		t.Fatalf("rosters after restart: A=%d B=%d", len(accountCharacters("1001")), len(accountCharacters("1002")))
	}
}

// The token is the only link from the D2R-services socket to the logon, so a
// client that writes another account's number into it gets no account.
func TestForgedSessionTokenOwnsNothing(t *testing.T) {
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	resetCharacters()
	addCharacter("1001", d2rCharacter{Name: "Hosta", Class: 4})

	for _, token := range []string{
		"openpak:1001", // the v0.2.0 shape, unsigned
		"openpak:1001.AAAAAAAAAAAAAAAAAAAAAA",
		"openpak:1001." + d2rSessionMAC("1002"),
		"",
	} {
		s := signIn(t, "10.89.0.7:5000", token)
		if s.subject != "" {
			t.Fatalf("token %q proved an account", token)
		}
		if ids := rosterIDs(t, d2rCall(t, s, characterHash, charReadList, nil)); len(ids) != 0 {
			t.Fatalf("token %q read account 1001's roster", token)
		}
	}
	if got := d2rTokenSubject(d2rSessionToken("1001")); got != "1001" {
		t.Fatalf("a token we issued did not verify: %q", got)
	}
}

// A v0.2.0 store holds one roster with no owner. It is kept and served to
// nobody until BATTLENET_CHARACTER_LEGACY_OWNER names the owner.
func TestLegacySharedRosterIsNotServed(t *testing.T) {
	path := t.TempDir() + "/characters.json"
	t.Setenv("BATTLENET_CHARACTER_STORE", path)
	legacy := `{"next_id":3,"characters":[{"Name":"Old","Class":4,"ID":1,"Level":1},{"Name":"Older","Class":1,"ID":2,"Level":1}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	resetCharacters()
	loadCharacters()
	if len(accountCharacters("1001")) != 0 || len(accountCharacters("")) != 0 {
		t.Fatal("legacy roster served without an owner")
	}
	// Saving with the legacy roster still unowned keeps it on disk.
	addCharacter("1002", d2rCharacter{Name: "New", Class: 2})
	resetCharacters()
	t.Setenv("BATTLENET_CHARACTER_LEGACY_OWNER", "1001")
	loadCharacters()
	if got := accountCharacters("1001"); len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("legacy roster not assigned to its owner: %+v", got)
	}
	if got := accountCharacters("1002"); len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("other account's roster changed: %+v", got)
	}
}

// Two GameJoins before either client dials: each dial gets its own account's
// character, oldest first, never the other's.
func TestPendingJoinsQueuePerAccount(t *testing.T) {
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	resetCharacters()
	pendingMu.Lock()
	pendingJoins = nil
	pendingMu.Unlock()

	ca, _ := addCharacter("1001", d2rCharacter{Name: "Hosta", Class: 4})
	cb, _ := addCharacter("1002", d2rCharacter{Name: "Joina", Class: 1})
	rememberPendingJoin("1001", ca.ID, 5)
	rememberPendingJoin("1002", cb.ID, 5)
	// An ID from another account's roster resolves to nothing.
	rememberPendingJoin("1002", ca.ID, 5)

	first, second, third := takePendingJoin(), takePendingJoin(), takePendingJoin()
	if first.subject != "1001" || first.name != "Hosta" || second.subject != "1002" || second.name != "Joina" {
		t.Fatalf("joins out of order or mixed: %+v / %+v", first, second)
	}
	if third.name != "Player" || third.class != 0 {
		t.Fatalf("another account's character id leaked a character: %+v", third)
	}
	if empty := takePendingJoin(); empty.gameID != 0 || empty.name != "Player" {
		t.Fatalf("empty queue: %+v", empty)
	}
}

// gameList asks for the Game List the way the title does and returns the
// listed names with their GameListInfo fields 2 (players) and 3 (max).
func gameList(t *testing.T, s *gatewaySession) map[string][2]uint64 {
	t.Helper()
	return gameListWith(t, s, nil)
}

func gameListWith(t *testing.T, s *gatewaySession, req []byte) map[string][2]uint64 {
	t.Helper()
	body := d2rCall(t, s, gameManagementHash, gameListAll, req)
	if len(repeatedField(t, body, 1)) != 0 {
		t.Fatal("GameListResponse carries field 1, which the client never reads")
	}
	out := map[string][2]uint64{}
	for _, info := range repeatedField(t, body, 3) {
		v, d := decodeTestFields(t, info)
		out[string(d[6])] = [2]uint64{v[2], v[3]}
	}
	return out
}

func createGameAs(t *testing.T, s *gatewaySession, name, password string, maxPlayers uint64) uint32 {
	t.Helper()
	req := pbBytes(2, []byte(name))
	req = append(req, pbBytes(3, []byte(password))...)
	req = append(req, pbVarint(9, 255)...)
	req = append(req, pbVarint(10, maxPlayers)...)
	req = append(req, pbVarint(14, 0)...)
	v, _ := decodeTestFields(t, d2rCall(t, s, gameManagementHash, gameCreate, req))
	if v[5] == 0 {
		t.Fatalf("GameCreate %q refused", name)
	}
	return uint32(v[5])
}

// The joiner (another account, same address) sees the host's game and can
// join it by the name the list gave.
func TestGameListShowsAnotherAccountsGame(t *testing.T) {
	testGameEnv(t)
	host := signIn(t, "10.89.0.7:5000", d2rSessionToken("1001"))
	joiner := signIn(t, "10.89.0.7:5001", d2rSessionToken("1002"))
	d2rCall(t, host, gameVersionHash, gameSetGameVersion, pbBytes(1, []byte("1.0.34.0")))
	d2rCall(t, joiner, gameVersionHash, gameSetGameVersion, pbBytes(1, []byte("1.0.34.0")))

	if got := gameList(t, joiner); len(got) != 0 {
		t.Fatalf("empty lobby listed %v", got)
	}
	id := createGameAs(t, host, "_-1i16605", "", 8)
	seatGame(id, "1001") // the host reached the game server

	got := gameList(t, joiner)
	if pm, ok := got["_-1i16605"]; !ok || pm != [2]uint64{1, 8} {
		t.Fatalf("joiner's list = %v, want the host's game at 1/8", got)
	}

	// The listed name is the join key.
	join := append(pbVarint(1, 1), pbBytes(2, []byte("_-1i16605"))...)
	v, d := decodeTestFields(t, d2rCall(t, joiner, gameManagementHash, gameJoin, join))
	if string(d[1]) != defaultGameHost || v[4] != uint64(id) {
		t.Fatalf("join by listed name: host=%q id=%d, want %d", d[1], v[4], id)
	}

	// A second account cannot take over the name of a live game.
	if body := d2rCall(t, joiner, gameManagementHash, gameCreate, pbBytes(2, []byte("_-1i16605"))); len(body) != 0 {
		t.Fatalf("another account re-created a live game: %x", body)
	}
}

// Full, closed, password-protected and other-version games are not listed;
// a full or closed game cannot be joined either.
func TestFullOrClosedGameNotListed(t *testing.T) {
	testGameEnv(t)
	host := signIn(t, "10.89.0.7:5000", d2rSessionToken("1001"))
	other := signIn(t, "10.89.0.7:5002", d2rSessionToken("1003"))
	joiner := signIn(t, "10.89.0.7:5001", d2rSessionToken("1002"))
	joiner.gameVersion = "1.0.34.0"

	full := createGameAs(t, host, "fullgame", "", 1)
	seatGame(full, "1001")
	closed := createGameAs(t, other, "closedgame", "", 8)
	seatGame(closed, "1003")
	leaveGame(closed, "1003") // last player left: the game is over
	createGameAs(t, host, "lockedgame", "abc123", 8)
	other.gameVersion = "1.0.33.0"
	createGameAs(t, other, "oldgame", "", 8)
	open := createGameAs(t, host, "opengame", "", 8)
	seatGame(open, "1001")

	got := gameList(t, joiner)
	if len(got) != 1 {
		t.Fatalf("list = %v, want only opengame", got)
	}
	if _, ok := got["opengame"]; !ok {
		t.Fatalf("list = %v, want opengame", got)
	}

	for _, name := range []string{"fullgame", "closedgame"} {
		join := append(pbVarint(1, 1), pbBytes(2, []byte(name))...)
		if body := d2rCall(t, joiner, gameManagementHash, gameJoin, join); len(body) != 0 {
			t.Fatalf("joined %s: %x", name, body)
		}
	}
	// The password game is still joinable by name with its password.
	join := append(append(pbVarint(1, 1), pbBytes(2, []byte("lockedgame"))...), pbBytes(3, []byte("abc123"))...)
	if body := d2rCall(t, joiner, gameManagementHash, gameJoin, join); len(body) == 0 {
		t.Fatal("password game not joinable with its password")
	}
}

// A game nobody ever reached the game server for drops off after
// staleGameAfter.
func TestAbandonedGameExpires(t *testing.T) {
	testGameEnv(t)
	rec, _ := createGameRecord("10.89.0.7:5000", "1001", "", gameCreateRequest{Key: "ghost", MaxPlayers: 8})
	if len(listedGames("")) != 1 {
		t.Fatal("fresh game not listed")
	}
	gameMu.Lock()
	rec.CreatedAt = rec.CreatedAt.Add(-staleGameAfter - time.Second)
	gameMu.Unlock()
	if got := listedGames(""); len(got) != 0 {
		t.Fatalf("abandoned game still listed: %+v", got)
	}
	if _, ok := lookupGame("ghost"); ok {
		t.Fatal("abandoned game still joinable")
	}
}

// The response is exactly the recovered GameListResponse shape: the helper's
// probe row 1a{10:1,18:8,32:"probe",48:1,68:1}.
func TestGameListBodyShape(t *testing.T) {
	body := gameListBody([]gameRecord{{DisplayName: "probe", MaxPlayers: 8, Players: 1, Difficulty: 1, Desecrated: true}}, "")
	want := []byte{0x1a, 0x0f, 0x10, 0x01, 0x18, 0x08, 0x32, 0x05, 'p', 'r', 'o', 'b', 'e', 0x48, 0x01, 0x68, 0x01}
	if string(body) != string(want) {
		t.Fatalf("GameListResponse\n got %x\nwant %x", body, want)
	}
	if len(gameListBody(nil, "")) != 0 {
		t.Fatal("an empty list must be an empty body")
	}
	// The viewer's site goes back in field 8 (its best ping bucket), and a
	// game with no max still gets one: max 0 hides the row.
	v, d := decodeTestFields(t, repeatedField(t, gameListBody([]gameRecord{{DisplayName: "g"}}, "us-east"), 3)[0])
	if string(d[8]) != "us-east" || v[3] <= v[2] {
		t.Fatalf("site=%q players=%d max=%d", d[8], v[2], v[3])
	}
}

// The request's filters: difficulty masks, hardcore/ladder, game type,
// desecrated, name search; the site echoes back.
func TestGameListAppliesRequestFilters(t *testing.T) {
	testGameEnv(t)
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	resetCharacters()
	host := signIn(t, "10.89.0.7:5000", d2rSessionToken("1001"))
	joiner := signIn(t, "10.89.0.7:5001", d2rSessionToken("1002"))

	mk := func(name string, difficulty, charFlags uint64) {
		c, _ := addCharacter("1001", d2rCharacter{Name: "Hosta", Class: 4, Flags: charFlags})
		req := append(append(pbBytes(2, []byte(name)), pbVarint(10, 8)...), pbVarint(14, difficulty)...)
		v, _ := decodeTestFields(t, d2rCall(t, host, gameManagementHash, gameCreate, req))
		join := append(pbVarint(1, c.ID), pbBytes(2, []byte(name))...)
		d2rCall(t, host, gameManagementHash, gameJoin, join)
		seatGame(uint32(v[5]), "1001")
	}
	mk("normalsc", 0, 0x20)   // expansion softcore non-ladder, normal
	mk("hellhc", 2, 0x20|0x4) // expansion hardcore, hell
	mk("classicsc", 1, 0)     // classic softcore, nightmare
	mk("laddersc", 0, 0x20|0x40)

	names := func(req []byte) []string {
		var out []string
		for n := range gameListWith(t, joiner, req) {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	check := func(what string, req []byte, want ...string) {
		t.Helper()
		got := names(req)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: listed %v, want %v", what, got, want)
		}
	}
	check("no filter", nil, "classicsc", "hellhc", "laddersc", "normalsc")
	check("normal only", pbVarint(7, 1), "laddersc", "normalsc")
	check("masks intersect", append(pbVarint(7, 7), pbVarint(10, 6)...), "classicsc", "hellhc")
	check("softcore non-ladder expansion viewer", append(pbVarint(4, 0x20), pbVarint(12, 2)...), "normalsc")
	check("hardcore viewer", pbVarint(4, 0x24), "hellhc")
	check("classic", pbVarint(12, 1), "classicsc")
	check("only desecrated", pbVarint(11, 1))
	check("no desecrated", pbVarint(11, 0), "classicsc", "hellhc", "laddersc", "normalsc")
	check("name search", pbBytes(3, []byte("SC")), "classicsc", "laddersc", "normalsc")

	// The site comes back from the request: field 6, else the first field-9 entry.
	site := func(req []byte) string {
		body := d2rCall(t, joiner, gameManagementHash, gameListAll, req)
		_, d := decodeTestFields(t, repeatedField(t, body, 3)[0])
		return string(d[8])
	}
	lat := append(pbBytes(1, []byte("eu")), pbVarint(2, 30)...)
	if got := site(pbBytes(9, lat)); got != "eu" {
		t.Fatalf("site from the ping list = %q", got)
	}
	if got := site(append(pbBytes(6, []byte("us")), pbBytes(9, lat)...)); got != "us" {
		t.Fatalf("preferred site = %q", got)
	}
}
