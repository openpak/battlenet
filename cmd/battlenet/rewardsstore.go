package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	emit "github.com/openpak/rewards-emit"
)

// rewardsStore keeps the rewards state that must survive a restart in Postgres: which Switch BAAS
// user id each Battle.net subject belongs to, and the session clock (who has been seated together
// since when, and who was already counted). BATTLENET_DATABASE_URL names the database (a small
// pool, 3 connections at most).
type rewardsStore struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	last string // last saved clock, to skip unchanged saves
}

const rewardsSchema = `
CREATE TABLE IF NOT EXISTS baas_map (
  battlenet_subject text PRIMARY KEY,
  baas_user_id      text NOT NULL,
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS session_state (
  metric     text PRIMARY KEY,
  state      text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);`

func openRewardsStore(ctx context.Context, url string) (*rewardsStore, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("BATTLENET_DATABASE_URL: invalid")
	}
	cfg.MaxConns = 3
	var pool *pgxpool.Pool
	// Postgres can come up after this unit; wait for it rather than run without a store.
	for i := 0; ; i++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		if i >= 30 {
			return nil, fmt.Errorf("rewards store unreachable: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
	if _, err := pool.Exec(ctx, rewardsSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("rewards store schema: %w", err)
	}
	return &rewardsStore{pool: pool}, nil
}

func (s *rewardsStore) Close() { s.pool.Close() }

// loadMap returns every stored battlenet subject -> BAAS user id.
func (s *rewardsStore) loadMap(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT battlenet_subject, baas_user_id FROM baas_map`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *rewardsStore) saveMap(ctx context.Context, subject, baas string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO baas_map (battlenet_subject, baas_user_id) VALUES ($1, $2)
		ON CONFLICT (battlenet_subject) DO UPDATE SET baas_user_id = EXCLUDED.baas_user_id, updated_at = now()`,
		subject, baas)
	return err
}

func (s *rewardsStore) loadState(ctx context.Context, metric string) (emit.State, error) {
	var raw string
	var st emit.State
	err := s.pool.QueryRow(ctx, `SELECT state FROM session_state WHERE metric = $1`, metric).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return st, nil
		}
		return st, err
	}
	return st, json.Unmarshal([]byte(raw), &st)
}

// saveState writes the clock when it changed since the last save.
func (s *rewardsStore) saveState(ctx context.Context, metric string, st emit.State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(raw) == s.last {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_state (metric, state) VALUES ($1, $2)
		ON CONFLICT (metric) DO UPDATE SET state = EXCLUDED.state, updated_at = now()`, metric, string(raw)); err != nil {
		return err
	}
	s.last = string(raw)
	return nil
}

// rewardsDB is the process-wide store; nil only when BATTLENET_DATABASE_URL is unset (local runs).
var rewardsDB *rewardsStore

const d2rMetric = "game_sessions.diablo-ii-resurrected"

// startRewards opens the store, reloads the id map and the session clock, and starts the watcher.
func startRewards(ctx context.Context) {
	rw := emit.FromEnv("battlenet")
	s := emit.NewSessions(rw, d2rMetric, 0)
	if url := os.Getenv("BATTLENET_DATABASE_URL"); url != "" {
		st, err := openRewardsStore(ctx, url)
		if err != nil {
			log.Fatalf("rewards store: %v", err)
		}
		rewardsDB = st
		m, err := st.loadMap(ctx)
		if err != nil {
			log.Fatalf("rewards store: load map: %v", err)
		}
		for k, v := range m {
			baasBySubject.Store(k, v)
		}
		state, err := st.loadState(ctx, d2rMetric)
		if err != nil {
			log.Fatalf("rewards store: load state: %v", err)
		}
		s.Restore(state)
		s.SetOnTick(func(state emit.State) {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := st.saveState(c, d2rMetric, state); err != nil {
				log.Printf("rewards store: save state: %v", err)
			}
		})
		log.Printf("rewards store: %d id mappings restored", len(m))
	}
	s.Watch(ctx, 20*time.Second, rewardsRooms)
}
