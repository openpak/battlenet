package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	emit "github.com/openpak/rewards-emit"
)

func TestD2RSessionsEmitAfterThreeMinutesSeated(t *testing.T) {
	testGameEnv(t)
	baasBySubject = sync.Map{}
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct{ Events []map[string]any }
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		got = append(got, body.Events...)
		mu.Unlock()
	}))
	defer srv.Close()
	rw := emit.New(emit.Config{URL: srv.URL, Key: "k", Source: "unit", Flush: 10 * time.Millisecond})
	s := emit.NewSessions(rw, "game_sessions.diablo-ii-resurrected", 3*time.Minute)

	rememberBaas("100", "baas-a")
	rememberBaas("200", "baas-b")
	rememberBaas("300", "baas-c") // never seated
	rec := mustCreateGame(t, "r", gameCreateRequest{Key: "Room"})
	seatGame(rec.ID, "100")
	t0 := time.Now()
	if n := s.Tick(t0, rewardsRooms()); n != 0 {
		t.Fatalf("alone emitted %d", n)
	}
	seatGame(rec.ID, "200")
	seatGame(rec.ID, "999") // unmapped account does not count
	s.Tick(t0.Add(time.Minute), rewardsRooms())
	if n := s.Tick(t0.Add(3*time.Minute), rewardsRooms()); n != 0 {
		t.Fatalf("emitted before 3 min together: %d", n)
	}
	if n := s.Tick(t0.Add(4*time.Minute+time.Second), rewardsRooms()); n != 2 {
		t.Fatalf("want 2 events, got %d", n)
	}
	if n := s.Tick(t0.Add(10*time.Minute), rewardsRooms()); n != 0 {
		t.Fatalf("re-emitted: %d", n)
	}
	rw.Close(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("server got %d events", len(got))
	}
	for _, e := range got {
		if e["metric"] != "game_sessions.diablo-ii-resurrected" {
			t.Fatalf("metric %v", e["metric"])
		}
	}
}
