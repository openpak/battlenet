package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type fakeLinks map[string][2][]string // account -> {switch, battlenet}

func (f fakeLinks) accountSubjects(_ context.Context, id string) ([]string, []string, error) {
	return f[id][0], f[id][1], nil
}

const (
	eraseA = "11111111-1111-1111-1111-111111111111"
	eraseB = "22222222-2222-2222-2222-222222222222"
)

func eraseFixture(t *testing.T) fakeLinks {
	t.Helper()
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	rewardsDB = nil
	resetCharacters()
	baasBySubject.Range(func(k, _ any) bool { baasBySubject.Delete(k); return true })
	charMu.Lock()
	charRosters["100"] = []d2rCharacter{{ID: 1, Name: "A1"}, {ID: 2, Name: "A2"}}
	charRosters["200"] = []d2rCharacter{{ID: 3, Name: "B1"}}
	saveCharactersLocked()
	charMu.Unlock()
	baasBySubject.Store("100", "baas-a")
	baasBySubject.Store("200", "baas-b")
	pendingMu.Lock()
	pendingJoins = []pendingJoin{{subject: "100"}, {subject: "200"}}
	pendingMu.Unlock()
	return fakeLinks{eraseA: {{"baas-a"}, {"100"}}, eraseB: {{"baas-b"}, {"200"}}}
}

func TestEraseAccountOnlyThatAccount(t *testing.T) {
	l := eraseFixture(t)
	n, err := eraseAccount(context.Background(), l, eraseA)
	if err != nil || n != 4 { // 2 characters, id map, pending join
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(accountCharacters("100")) != 0 || len(accountCharacters("200")) != 1 {
		t.Fatal("rosters wrong")
	}
	if _, ok := baasBySubject.Load("100"); ok {
		t.Fatal("id map survived")
	}
	if _, ok := baasBySubject.Load("200"); !ok || len(pendingJoins) != 1 {
		t.Fatal("other account touched")
	}
	blob, _ := os.ReadFile(charStorePath())
	if len(blob) == 0 || string(blob) == "" || contains(blob, "A1") || !contains(blob, "B1") {
		t.Fatalf("file store not rewritten: %s", blob)
	}
	if n, err := eraseAccount(context.Background(), l, eraseA); err != nil || n != 0 {
		t.Fatalf("second run n=%d err=%v", n, err)
	}
}

func contains(b []byte, s string) bool { return len(s) > 0 && string(b) != "" && indexOf(string(b), s) >= 0 }

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestEraseEndpointAuth(t *testing.T) {
	l := eraseFixture(t)
	call := func(key, hdr, id string) int {
		mux := http.NewServeMux()
		mux.HandleFunc("DELETE /internal/accounts/{account_id}/data", eraseHandler(key, func() linkLister { return l }))
		r := httptest.NewRequest("DELETE", "/internal/accounts/"+id+"/data", nil)
		if hdr != "" {
			r.Header.Set("Authorization", hdr)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		key, hdr, id string
		want         int
	}{
		{"", "Bearer k", eraseA, 503},
		{"k", "", eraseA, 401},
		{"k", "Bearer nope", eraseA, 401},
		{"k", "Bearer k", "x", 400},
		{"k", "Bearer k", eraseA, 200},
	} {
		if got := call(c.key, c.hdr, c.id); got != c.want {
			t.Errorf("%+v: got %d", c, got)
		}
	}
}
