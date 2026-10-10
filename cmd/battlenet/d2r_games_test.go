package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"
)

func testGameEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BATTLENET_GAMEDETAIL_TUNE", t.TempDir()+"/missing-tune.json")
	t.Setenv("BATTLENET_GAMEMASTER_HOST", "")
	t.Setenv("BATTLENET_GAMEMASTER_PORT", "")
	resetGameRegistry()
}

// mustCreateGame records a game for a fixed test account.
func mustCreateGame(t *testing.T, remote string, req gameCreateRequest) *gameRecord {
	t.Helper()
	rec, ok := createGameRecord(remote, "100", "", req)
	if !ok {
		t.Fatalf("create %q refused", req.Key)
	}
	return rec
}

func encodeGameCreate(key, password string) []byte {
	out := pbBytes(2, []byte(key))
	out = append(out, pbBytes(3, []byte(password))...)
	out = append(out, pbVarint(9, 8)...)
	out = append(out, pbVarint(10, 3)...)
	out = append(out, pbVarint(11, 4325376)...)
	out = append(out, pbVarint(14, 0)...)
	return append(out, pbVarint(15, 1)...)
}

func encodeGameJoin(key, password string) []byte {
	out := pbVarint(1, 1)
	out = append(out, pbBytes(2, []byte(key))...)
	out = append(out, pbBytes(3, []byte(password))...)
	return append(out, pbBytes(4, []byte{})...)
}

func TestParseGameCreate(t *testing.T) {
	req := parseGameCreate(encodeGameCreate("hansolo's game", "abc123"))
	if req.Key != "hansolo's game" || req.Password != "abc123" {
		t.Fatalf("create parse: %+v", req)
	}
	if req.LevelDiff != 8 || req.MaxPlayers != 3 || req.Flags != 4325376 || req.Difficulty != 0 {
		t.Fatalf("create descriptive fields: %+v", req)
	}
}

func TestParseGameJoin(t *testing.T) {
	req := parseGameJoin(encodeGameJoin("hansolo's game", "abc123"))
	if req.Key != "hansolo's game" || req.Password != "abc123" || req.CharacterID != 1 {
		t.Fatalf("join parse: %+v", req)
	}
}

func TestGameIDAllocationAndLookup(t *testing.T) {
	testGameEnv(t)
	rec := mustCreateGame(t, "10.90.90.92:54321", parseGameCreate(encodeGameCreate("mygame", "")))
	if rec.ID == 0 {
		t.Fatal("allocated game ID must be nonzero")
	}
	if rec.Host != defaultGameHost {
		t.Fatalf("host = %s, want the game-server name the title dials on 443", rec.Host)
	}
	got, ok := lookupGame("mygame")
	if !ok || got.ID != rec.ID {
		t.Fatalf("lookup: %+v ok=%v want id %d", got, ok, rec.ID)
	}
}

func TestTwoGamesGetDistinctIDs(t *testing.T) {
	testGameEnv(t)
	a := mustCreateGame(t, "10.0.0.1:1111", parseGameCreate(encodeGameCreate("gameone", "")))
	b := mustCreateGame(t, "10.0.0.1:2222", parseGameCreate(encodeGameCreate("gametwo", "")))
	if a.ID == 0 || b.ID == 0 || a.ID == b.ID {
		t.Fatalf("IDs not distinct: %d vs %d", a.ID, b.ID)
	}
}

func TestNormalizedNameLookup(t *testing.T) {
	testGameEnv(t)
	rec := mustCreateGame(t, "10.0.0.1:1111", parseGameCreate(encodeGameCreate("hansolo's game", "")))
	for _, variant := range []string{"Hansolo's Game", "HANSOLO'S GAME", "hansolo's game"} {
		got, ok := lookupGame(variant)
		if !ok || got.ID != rec.ID {
			t.Fatalf("lookup %q failed (ok=%v)", variant, ok)
		}
	}
}

func TestPasswordAcceptanceAndRejection(t *testing.T) {
	testGameEnv(t)
	mustCreateGame(t, "10.0.0.1:1111", parseGameCreate(encodeGameCreate("locked", "abc123")))
	rec, ok := lookupGame("locked")
	if !ok {
		t.Fatal("game not found")
	}
	if !checkGamePassword(rec, "abc123") {
		t.Fatal("correct password rejected")
	}
	if checkGamePassword(rec, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if checkGamePassword(rec, "") {
		t.Fatal("empty password accepted for locked game")
	}

	mustCreateGame(t, "10.0.0.1:1111", parseGameCreate(encodeGameCreate("open", "")))
	open, ok := lookupGame("open")
	if !ok || !checkGamePassword(open, "") || !checkGamePassword(open, "anything") {
		t.Fatalf("open game should accept any password: %+v", open)
	}
}

func TestGameMasterHostExtraction(t *testing.T) {
	if got := gameMasterHost("10.90.90.92:54321"); got != "10.90.90.92" {
		t.Fatalf("ipv4 = %q", got)
	}
	if got := gameMasterHost("[fd00::1]:54321"); got != "fd00::1" {
		t.Fatalf("ipv6 = %q", got)
	}
	if got := gameMasterHost("192.168.1.10:21501"); got == "192.168.1.10:21501" {
		t.Fatalf("TCP source port leaked into host: %q", got)
	}
}

func TestGameMasterEndpointOverride(t *testing.T) {
	testGameEnv(t)
	t.Setenv("BATTLENET_GAMEMASTER_HOST", "203.0.113.7")
	t.Setenv("BATTLENET_GAMEMASTER_PORT", "6113")
	host, port := gameMasterEndpoint("10.0.0.1:9999")
	if host != "203.0.113.7" || port != 6113 {
		t.Fatalf("override endpoint = %s:%d", host, port)
	}
	rec := mustCreateGame(t, "10.0.0.1:9999", parseGameCreate(encodeGameCreate("natgame", "")))
	if rec.Host != "203.0.113.7" || rec.Port != 6113 {
		t.Fatalf("record did not use override: %+v", rec)
	}
}

func TestGameJoinResponseFields(t *testing.T) {
	body := gameJoinBody("d2gs.battle.net", 99)
	varints, datas := decodeTestFields(t, body)
	if string(datas[1]) != "d2gs.battle.net" {
		t.Fatalf("join field 1 = %q, want host", datas[1])
	}
	if varints[3] != 99 || varints[4] != 99 {
		t.Fatalf("join fields 3/4 = %d/%d, want the game id in both", varints[3], varints[4])
	}
	if _, ok := varints[6]; ok {
		t.Fatalf("join field 6 selects the queued path and must be absent")
	}
}

func TestDynamicCreateJoinFlow(t *testing.T) {
	testGameEnv(t)
	creator := &gatewaySession{remote: "10.90.90.92:51234", subject: "100"}
	createBody, ok := handleGameRPC(creator, d2rHeader{
		service: gameManagementHash, method: gameCreate, body: encodeGameCreate("hansolo's game", "abc123"),
	})
	if !ok || len(createBody) == 0 {
		t.Fatal("dynamic GameCreate must answer when no raw-hex override exists")
	}
	joiner := &gatewaySession{remote: "10.90.90.93:60000", subject: "200"}
	joinBody, ok := handleGameRPC(joiner, d2rHeader{
		service: gameManagementHash, method: gameJoin, body: encodeGameJoin("HANSOLO'S GAME", "abc123"),
	})
	if !ok || len(joinBody) == 0 {
		t.Fatal("dynamic GameJoin must answer with correct password and normalized name")
	}
	varints, datas := decodeTestFields(t, joinBody)
	if string(datas[1]) != defaultGameHost {
		t.Fatalf("join host = %q, want the game-server name", datas[1])
	}
	if varints[3] == 0 || varints[4] == 0 {
		t.Fatalf("join fields 3/4 must carry the game id: %v", varints)
	}
	rec, _ := lookupGame("hansolo's game")
	if varints[4] != uint64(rec.ID) {
		t.Fatalf("join field 4 = %d, want allocated id %d", varints[4], rec.ID)
	}

	bad, ok := handleGameRPC(joiner, d2rHeader{
		service: gameManagementHash, method: gameJoin, body: encodeGameJoin("hansolo's game", "wrong"),
	})
	if ok || len(bad) != 0 {
		t.Fatal("wrong password must not return a join body")
	}
}

func TestRawHexOverridePrecedence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tune.json"
	if err := os.WriteFile(path, []byte(`{"create_hex":"2801","join_hex":"3001"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATTLENET_GAMEDETAIL_TUNE", path)
	t.Setenv("BATTLENET_GAMEMASTER_HOST", "")
	t.Setenv("BATTLENET_GAMEMASTER_PORT", "")
	resetGameRegistry()

	creator := &gatewaySession{remote: "10.0.0.1:1111", subject: "100"}
	body, ok := handleGameRPC(creator, d2rHeader{
		service: gameManagementHash, method: gameCreate, body: encodeGameCreate("any", ""),
	})
	if !ok || fmt.Sprintf("%x", body) != "2801" {
		t.Fatalf("create_hex override not used: %x ok=%v", body, ok)
	}
	joinBody, ok := handleGameRPC(creator, d2rHeader{
		service: gameManagementHash, method: gameJoin, body: encodeGameJoin("any", ""),
	})
	if !ok || fmt.Sprintf("%x", joinBody) != "3001" {
		t.Fatalf("join_hex override not used: %x ok=%v", joinBody, ok)
	}
}

func TestConcurrentCreateJoin(t *testing.T) {
	testGameEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("game-%d", i%4)
			rec, _ := createGameRecord("10.0.0.5:4000", "100", "", parseGameCreate(encodeGameCreate(key, "")))
			if _, ok := lookupGame(key); !ok {
				t.Errorf("lookup %q failed", key)
			}
			_ = gameJoinBody(rec.Host, rec.ID)
			_, _ = handleGameRPC(&gatewaySession{remote: "10.0.0.6:5000", subject: "200"}, d2rHeader{
				service: gameManagementHash, method: gameJoin, body: encodeGameJoin(key, ""),
			})
		}(i)
	}
	wg.Wait()
}

// The bytes the live client sends: 0a0e "Game1628292938".
func TestGameDetailProbeEchoesTheName(t *testing.T) {
	req := pbBytes(1, []byte("Game1628292938"))

	if got := gameDetailName(req); got != "Game1628292938" {
		t.Fatalf("name = %q", got)
	}

	t.Setenv("BATTLENET_GAMEDETAIL_TUNE", t.TempDir()+"/tune.json")
	body, _ := gameDetailBody("Game1628292938")

	// Field 1 echoes the name the client asked about, and the two markers go
	// out in distinct fields so the client's dial identifies which is the host.
	if !bytes.Contains(body, []byte("Game1628292938")) {
		t.Fatalf("name missing: %x", body)
	}
	// With no tune file the body is just the echoed name: an absent probe
	// must not invent field values.
	if !bytes.Equal(body, pbBytes(1, []byte("Game1628292938"))) {
		t.Fatalf("untuned body should be the name alone, got %x", body)
	}
}
