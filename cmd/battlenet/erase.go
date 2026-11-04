package main

// Account erasure (openpak/prds#120). DELETE /internal/accounts/{account_id}/data with
// `Authorization: Bearer <ACCOUNT_ERASE_KEY>` removes everything this service holds for the
// OpenPak account: the D2R character roster, the battlenet subject -> BAAS id map and whatever
// live state (game seats, pending joins) names the account. Served on BATTLENET_ERASE_ADDR
// (default :21502), container network only. Unset key: 503.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
)

const eraseService = "battlenet"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// linkLister returns the subjects the core still holds for an account, by namespace.
type linkLister interface {
	accountSubjects(ctx context.Context, accountID string) (switchSubs, battlenetSubs []string, err error)
}

func (c *identityClient) accountSubjects(ctx context.Context, accountID string) (sw, bn []string, err error) {
	resp, err := c.links.ListAccountLinks(ctx, &accountv1.ListAccountLinksRequest{AccountId: accountID})
	if err != nil {
		return nil, nil, err
	}
	for _, l := range resp.GetLinks() {
		switch l.GetNamespace() {
		case switchNamespace:
			sw = append(sw, l.GetSubjectId())
		case battlenetNamespace:
			bn = append(bn, l.GetSubjectId())
		}
	}
	return sw, bn, nil
}

// eraseAccount deletes the account's data and returns how many rows/entries went.
func eraseAccount(ctx context.Context, lister linkLister, accountID string) (int, error) {
	if lister == nil {
		return 0, errors.New("account core not configured")
	}
	sw, bn, err := lister.accountSubjects(ctx, accountID)
	if err != nil {
		return 0, err
	}
	rows := 0
	isBN := map[string]bool{}
	for _, s := range bn {
		isBN[s] = true
	}
	isSW := map[string]bool{}
	for _, s := range sw {
		isSW[s] = true
	}

	// Rosters. The database is rewritten from memory on every save, so delete the rows first
	// and only then memory, under the roster lock.
	charMu.Lock()
	if rewardsDB != nil && len(bn) > 0 {
		tag, err := rewardsDB.pool.Exec(ctx, `DELETE FROM d2r_characters WHERE subject = ANY($1)`, bn)
		if err != nil {
			charMu.Unlock()
			return rows, err
		}
		rows += int(tag.RowsAffected())
		for _, s := range bn {
			delete(charRosters, s)
		}
	} else {
		removed := 0
		for _, s := range bn {
			removed += len(charRosters[s])
			delete(charRosters, s)
		}
		rows += removed
		if removed > 0 {
			saveCharactersLocked() // file mode: rewrite characters.json without them
		}
	}
	charMu.Unlock()

	// Id map: battlenet subject -> BAAS id (durable and cached).
	if rewardsDB != nil {
		tag, err := rewardsDB.pool.Exec(ctx,
			`DELETE FROM baas_map WHERE battlenet_subject = ANY($1) OR baas_user_id = ANY($2)`, bn, sw)
		if err != nil {
			return rows, err
		}
		rows += int(tag.RowsAffected())
	}
	baasBySubject.Range(func(k, v any) bool {
		if isBN[k.(string)] || isSW[v.(string)] {
			baasBySubject.Delete(k)
			if rewardsDB == nil {
				rows++
			}
		}
		return true
	})

	// Live state: seats, owned game records, pending joins. Ephemeral, but it names the account.
	gameMu.Lock()
	for key, rec := range gamesByKey {
		for s := range rec.Seated {
			if isBN[s] {
				delete(rec.Seated, s)
				rows++
			}
		}
		if isBN[rec.Owner] {
			rec.Owner = ""
		}
		if rec.EverSeated && len(rec.Seated) == 0 {
			delete(gamesByKey, key)
		}
	}
	gameMu.Unlock()
	pendingMu.Lock()
	kept := pendingJoins[:0]
	for _, j := range pendingJoins {
		if isBN[j.subject] {
			rows++
		} else {
			kept = append(kept, j)
		}
	}
	pendingJoins = kept
	pendingMu.Unlock()

	log.Printf("account erased account=%s rows=%d service=%s", accountID, rows, eraseService)
	return rows, nil
}

func eraseHandler(key string, lister func() linkLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reply := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if key == "" {
			reply(503, map[string]any{"error": "erase not configured"})
			return
		}
		hdr := r.Header.Get("Authorization")
		got := strings.TrimPrefix(hdr, "Bearer ")
		if got == hdr || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			reply(401, map[string]any{"error": "unauthorized"})
			return
		}
		id := r.PathValue("account_id")
		if !uuidRe.MatchString(id) {
			reply(400, map[string]any{"error": "account_id must be a UUID"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		rows, err := eraseAccount(ctx, lister(), strings.ToLower(id))
		if err != nil {
			log.Printf("erase failed: %v", err)
			reply(500, map[string]any{"error": "erase failed"})
			return
		}
		reply(200, map[string]any{"service": eraseService, "rows": rows})
	}
}

// serveErase starts the erase listener when ACCOUNT_ERASE_KEY is set.
func serveErase() {
	key := os.Getenv("ACCOUNT_ERASE_KEY")
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /internal/accounts/{account_id}/data", eraseHandler(key, func() linkLister {
		l, _ := identities.(linkLister)
		return l
	}))
	if key == "" {
		log.Printf("ACCOUNT_ERASE_KEY unset: erase endpoint answers 503")
	}
	addr := env("BATTLENET_ERASE_ADDR", ":21502")
	go func() {
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		log.Printf("erase endpoint on %s", addr)
		log.Printf("erase endpoint stopped: %v", srv.ListenAndServe())
	}()
}
