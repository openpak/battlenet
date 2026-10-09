package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testIDToken builds an unsigned three-part token carrying the version claims
// (the signature is checked by the identity exchange, not here).
func testIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// fakeNXBaaS answers /internal/title-version like nx-baas: latest 00200000 for
// D2R, update_required below it. It insists on the internal key.
func fakeNXBaaS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/title-version" || r.Header.Get("X-Internal-Key") != "k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		title, version := r.URL.Query().Get("title"), r.URL.Query().Get("version")
		json.NewEncoder(w).Encode(map[string]any{
			"title": title, "version": version, "latest": "00200000",
			"update_required": title == "0100726014352000" && version < "00200000",
		})
	}))
}

func TestCheckTitleVersion(t *testing.T) {
	srv := fakeNXBaaS(t)
	defer srv.Close()

	old := testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000", "nintendo.av": "001f0000"})
	v, err := checkTitleVersion(context.Background(), srv.URL, "k", old)
	if err != nil || !v.UpdateRequired || v.Latest != "00200000" || v.Version != "001f0000" || v.Title != "0100726014352000" {
		t.Fatalf("outdated: %+v err=%v", v, err)
	}

	cur := testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000", "nintendo.av": "00200000"})
	if v, err := checkTitleVersion(context.Background(), srv.URL, "k", cur); err != nil || v.UpdateRequired {
		t.Fatalf("current: %+v err=%v", v, err)
	}
}

// Everything that cannot be read or answered is allowed.
func TestCheckTitleVersionFailsOpen(t *testing.T) {
	srv := fakeNXBaaS(t)
	defer srv.Close()
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{")) }))
	defer garbage.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	saved := versionTimeout
	versionTimeout = 100 * time.Millisecond
	defer func() { versionTimeout = saved }()

	old := testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000", "nintendo.av": "001f0000"})
	for name, c := range map[string]struct{ url, key, token string }{
		"endpoint not live (404)": {notFound.URL, "k", old},
		"wrong key (401)":         {srv.URL, "wrong", old},
		"unparsable answer":       {garbage.URL, "k", old},
		"timeout":                 {slow.URL, "k", old},
		"unreachable":             {"http://127.0.0.1:1", "k", old},
		"no av claim":             {srv.URL, "k", testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000"})},
		"not a JWT":               {srv.URL, "k", "nope"},
	} {
		v, err := checkTitleVersion(context.Background(), c.url, c.key, c.token)
		if err == nil || v.UpdateRequired {
			t.Errorf("%s: want an error and allowed, got %+v err=%v", name, v, err)
		}
	}
}

func TestRecordLoginVersion(t *testing.T) {
	srv := fakeNXBaaS(t)
	defer srv.Close()
	t.Setenv("NX_INTERNAL_URL", srv.URL)
	t.Setenv("NX_INTERNAL_KEY", "k")

	recordLoginVersion("test", "111", testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000", "nintendo.av": "001f0000"}))
	if _, ok := outdated.Load("111"); !ok {
		t.Fatal("outdated login not recorded")
	}
	// The same player after updating: the mark is cleared.
	recordLoginVersion("test", "111", testIDToken(t, map[string]any{"nintendo.ai": "0100726014352000", "nintendo.av": "00200000"}))
	if _, ok := outdated.Load("111"); ok {
		t.Fatal("current login still marked outdated")
	}
}

// The LoginResponse is the same bytes whatever the version verdict: field 4
// is dropped by the title, so it is never written.
func TestLoginResponseUnchangedByVerdict(t *testing.T) {
	want := append(pbBytes(1, []byte("wss://us.actual.battle.net:1119")), pbBytes(2, []byte("openpak:222"))...)
	before := d2rLoginResponse("222")
	if got := loginResponse("222"); !bytes.Equal(got, want) {
		t.Fatalf("LoginResponse (current):\n got %x\nwant %x", got, want)
	}
	outdated.Store("222", versionVerdict{Title: "0100726014352000", Version: "001f0000", Latest: "00200000", UpdateRequired: true})
	defer outdated.Delete("222")
	if got := loginResponse("222"); !bytes.Equal(got, want) {
		t.Fatalf("LoginResponse (outdated):\n got %x\nwant %x", got, want)
	}
	if after := d2rLoginResponse("222"); !bytes.Equal(before, after) {
		t.Fatalf("ProcessTaskResponse changed:\n before %x\n after %x", before, after)
	}
}

func setGameVersionPacket(token uint32) []byte {
	hdr := pbVarint(1, uint64(gameVersionHash))
	hdr = append(hdr, pbVarint(2, uint64(gameSetGameVersion))...)
	hdr = append(hdr, pbVarint(3, uint64(token))...)
	body := pbBytes(1, []byte("1.0.34.0"))
	hdr = append(hdr, pbVarint(5, uint64(len(body)))...)
	packet := make([]byte, 2)
	binary.BigEndian.PutUint16(packet, uint16(len(hdr)))
	return append(append(packet, hdr...), body...)
}

func drain(s *gatewaySession) [][]byte {
	var frames [][]byte
	for {
		select {
		case f := <-s.out:
			frames = append(frames, f)
		default:
			return frames
		}
	}
}

func TestGameUpdateRequiredPush(t *testing.T) {
	if got := authSessionSubject(append(pbBytes(2, []byte("x")), pbBytes(1, []byte("openpak:333"))...)); got != "333" {
		t.Fatalf("AuthSession subject %q", got)
	}

	v := versionVerdict{Title: "0100726014352000", Version: "001f0000", Latest: "00200000", UpdateRequired: true}
	outdated.Store("333", v)
	defer outdated.Delete("333")

	// Knob off (the default): SetGameVersion gets its empty answer and nothing else.
	s := newGatewaySession("test")
	s.subject = "333"
	handleD2RServices(s, setGameVersionPacket(9))
	if frames := drain(s); len(frames) != 1 {
		t.Fatalf("knob off: %d frames, want 1", len(frames))
	}

	// Knob on: the answer, then the push with GameUpdateRequest field 1.
	t.Setenv("BATTLENET_GAME_UPDATE_PUSH", "1")
	handleD2RServices(s, setGameVersionPacket(10))
	frames := drain(s)
	if len(frames) != 2 {
		t.Fatalf("knob on: %d frames, want 2", len(frames))
	}
	h, ok := parseD2RHeader(frames[1])
	if !ok || h.service != gameVersionHash || h.method != gameUpdateRequired || bytes.Contains(frames[1], pbVarint(9, 1)) {
		t.Fatalf("push header %+v ok=%v frame %x", h, ok, frames[1])
	}
	want := pbBytes(1, []byte("Update required: version 001f0000 installed, 00200000 available"))
	if !bytes.Equal(h.body, want) {
		t.Fatalf("push body:\n got %x\nwant %x", h.body, want)
	}

	// A current player gets no push even with the knob on.
	s.subject = "444"
	handleD2RServices(s, setGameVersionPacket(11))
	if frames := drain(s); len(frames) != 1 {
		t.Fatalf("current player: %d frames, want 1", len(frames))
	}
}
