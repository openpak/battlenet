package main

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	emit "github.com/openpak/rewards-emit"
)

// TestRewardsStateSurvivesRestart needs a throwaway Postgres (TEST_DATABASE_URL); it is skipped without one.
func TestRewardsStateSurvivesRestart(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := openRewardsStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.pool.Exec(ctx, `TRUNCATE baas_map, session_state`)

	// First process: two players seated together for 2 minutes, id map learned at login.
	rewardsDB = st
	baasBySubject = sync.Map{}
	rememberBaas("100", "baas-a")
	rememberBaas("200", "baas-b")
	room := []emit.Room{{ID: "d2r:1:1", Players: []emit.Identity{{Namespace: "switch", Subject: "baas-a"}, {Namespace: "switch", Subject: "baas-b"}}}}
	rw := emit.New(emit.Config{}) // disabled; the clock still runs through Tick
	s1 := emit.NewSessions(rw, d2rMetric, 3*time.Minute)
	t0 := time.Now()
	s1.Tick(t0, room)
	s1.Tick(t0.Add(2*time.Minute), room)
	if err := st.saveState(ctx, d2rMetric, s1.State()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Restart: everything in memory is gone.
	baasBySubject = sync.Map{}
	rewardsDB = nil
	st2, err := openRewardsStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	m, err := st2.loadMap(ctx)
	if err != nil || m["100"] != "baas-a" || m["200"] != "baas-b" {
		t.Fatalf("id map after restart: %v %v", m, err)
	}
	state, err := st2.loadState(ctx, d2rMetric)
	if err != nil {
		t.Fatal(err)
	}
	s2 := emit.NewSessions(rw, d2rMetric, 3*time.Minute)
	s2.Restore(state)
	// The clock resumed: 1 more minute completes the 3 that began before the restart.
	if n := s2.Tick(t0.Add(3*time.Minute+time.Second), room); n != 2 {
		t.Fatalf("after restart want 2 events, got %d", n)
	}
	// And a counted player stays counted across another restart.
	if err := st2.saveState(ctx, d2rMetric, s2.State()); err != nil {
		t.Fatal(err)
	}
	state, _ = st2.loadState(ctx, d2rMetric)
	s3 := emit.NewSessions(rw, d2rMetric, 3*time.Minute)
	s3.Restore(state)
	if n := s3.Tick(t0.Add(10*time.Minute), room); n != 0 {
		t.Fatalf("recounted after restart: %d", n)
	}
}
