package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// D2R rosters live in Postgres (BATTLENET_DATABASE_URL), one row per character, so one database
// backup covers them. characters.json is imported once and left in place.

const charSchema = `
CREATE TABLE IF NOT EXISTS d2r_characters (
  id      bigint PRIMARY KEY,
  subject text NOT NULL,            -- battlenet subject; '' = legacy character with no owner yet
  data    jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS d2r_characters_subject ON d2r_characters (subject);
CREATE TABLE IF NOT EXISTS d2r_char_state (
  key   text PRIMARY KEY,
  value text NOT NULL
);`

// loadCharactersDB fills the in-memory rosters from Postgres, importing the JSON file first on
// the very first start. Call with charMu held.
func loadCharactersDB(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := rewardsDB.pool
	if _, err := p.Exec(ctx, charSchema); err != nil {
		return fmt.Errorf("character schema: %w", err)
	}
	var done string
	err := p.QueryRow(ctx, `SELECT value FROM d2r_char_state WHERE key = 'json_imported'`).Scan(&done)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if done == "" {
		if blob, rerr := os.ReadFile(path); rerr == nil {
			var st charStore
			if err := json.Unmarshal(blob, &st); err != nil {
				return fmt.Errorf("parsing %s: %w", path, err)
			}
			charRosters, charUnowned, charNext = st.Accounts, st.Characters, st.Next
			if charRosters == nil {
				charRosters = map[string][]d2rCharacter{}
			}
			if charNext == 0 {
				charNext = 1
			}
			if err := saveCharactersDB(ctx); err != nil {
				return err
			}
			log.Printf("CHARACTERS: imported %d owned and %d unowned character(s) from %s into Postgres", countCharactersLocked(), len(charUnowned), path)
		} else if !errors.Is(rerr, os.ErrNotExist) {
			return rerr
		}
		if _, err := p.Exec(ctx, `INSERT INTO d2r_char_state (key, value) VALUES ('json_imported', 'yes') ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	charRosters = map[string][]d2rCharacter{}
	charUnowned = nil
	rows, err := p.Query(ctx, `SELECT subject, data FROM d2r_characters ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var subj, raw string
		if err := rows.Scan(&subj, &raw); err != nil {
			return err
		}
		var c d2rCharacter
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return err
		}
		if subj == "" {
			charUnowned = append(charUnowned, c)
		} else {
			charRosters[subj] = append(charRosters[subj], c)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var next string
	charNext = 1
	if err := p.QueryRow(ctx, `SELECT value FROM d2r_char_state WHERE key = 'next_id'`).Scan(&next); err == nil {
		if n, _ := strconv.ParseUint(next, 10, 64); n > 0 {
			charNext = n
		}
	}
	// next_id never falls below the highest stored id.
	for _, r := range charRosters {
		for _, c := range r {
			if c.ID >= charNext {
				charNext = c.ID + 1
			}
		}
	}
	return nil
}

// saveCharactersDB replaces the stored rosters with the in-memory ones in one transaction.
// Call with charMu held.
func saveCharactersDB(ctx context.Context) error {
	tx, err := rewardsDB.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM d2r_characters`); err != nil {
		return err
	}
	ins := func(subj string, c d2rCharacter) error {
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO d2r_characters (id, subject, data) VALUES ($1,$2,$3)`, int64(c.ID), subj, string(raw))
		return err
	}
	for subj, r := range charRosters {
		for _, c := range r {
			if err := ins(subj, c); err != nil {
				return err
			}
		}
	}
	for _, c := range charUnowned {
		if err := ins("", c); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO d2r_char_state (key, value) VALUES ('next_id', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, strconv.FormatUint(charNext, 10)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
