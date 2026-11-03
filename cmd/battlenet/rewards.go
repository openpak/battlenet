package main

import (
	"fmt"
	"sync"

	emit "github.com/openpak/rewards-emit"
)

// Rewards sessions (metric game_sessions.diablo-ii-resurrected): a player counts once per D2R
// game after 3 unbroken minutes seated in it with at least one other player. Seats are the
// game-server connections the server itself holds (seatGame/leaveGame), so only what this host
// observed is counted. The identity is the player's Switch BAAS user id (the switch link subject,
// proven by the verified id_token at check_token), never the Battle.net subject.

// baasBySubject maps a battlenet subject to the BAAS user id its login proved.
var baasBySubject sync.Map

func rememberBaas(battlenetSubject, baasUserID string) {
	if battlenetSubject != "" && baasUserID != "" {
		baasBySubject.Store(battlenetSubject, baasUserID)
	}
}

// rewardsRooms is the Sessions snapshot: every live game with its seated, mapped players. The id
// carries the creation time because game ids restart at 1 with the process.
func rewardsRooms() []emit.Room {
	gameMu.Lock()
	defer gameMu.Unlock()
	rooms := make([]emit.Room, 0, len(gamesByKey))
	for _, rec := range gamesByKey {
		r := emit.Room{ID: fmt.Sprintf("d2r:%d:%d", rec.ID, rec.CreatedAt.UnixNano())}
		for subject := range rec.Seated {
			if v, ok := baasBySubject.Load(subject); ok {
				r.Players = append(r.Players, emit.Identity{Namespace: "switch", Subject: v.(string)})
			}
		}
		rooms = append(rooms, r)
	}
	return rooms
}
