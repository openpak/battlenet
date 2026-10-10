package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"openpak/battlenet/redact"
)

// Version check for D2R logins. nx-baas owns the latest-version table and
// already refuses outdated titles at aauth (0111, the console's 2124-3001);
// this asks it the same question for the id_token the title signs in with, so
// the game server knows whether the player runs an outdated build.
//
// What the title does with an answer (update build DB0507F3..., disassembly in
// private scratch):
//   - LoginResponse field 4 (GameUpdateRequest) is parsed into a stack local
//     and dropped; only fields 1 and 2 are copied out. It changes nothing.
//   - GameVersion.GameUpdateRequired (service 0x3d930f0e, method 0x01b1b8b2) is
//     a server push on the D2R-services socket. The handler stores the
//     GameUpdateRequest {1 string, 5 u64} and notifies its listeners;
//     "update required" reads as field 1 being non-empty. What the UI then
//     shows is not traced yet.
//
// So the verdict is logged on every login, the LoginResponse is never
// changed, and the push stays behind BATTLENET_GAME_UPDATE_PUSH until a run
// against the title shows what it does. Fail open: anything that cannot be
// read or answered means allowed.
const gameUpdateRequired = uint32(0x01b1b8b2) // GameVersion.GameUpdateRequired (push)

// versionTimeout bounds the nx-baas question; a var so tests can shorten it.
var versionTimeout = 3 * time.Second

type versionVerdict struct {
	Title, Version, Latest string
	UpdateRequired         bool
}

// titleVersionClaims reads nintendo.ai (title id) and nintendo.av (installed
// version, "%08x") from the id_token payload. The signature was checked by the
// identity exchange before this runs.
func titleVersionClaims(idToken string) (title, version string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		AI string `json:"nintendo.ai"`
		AV string `json:"nintendo.av"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return claims.AI, claims.AV
}

// checkTitleVersion asks nx-baas whether this install needs an update. Every
// failure returns allowed, with the reason for the one log line.
func checkTitleVersion(ctx context.Context, baseURL, key, idToken string) (versionVerdict, error) {
	title, version := titleVersionClaims(idToken)
	v := versionVerdict{Title: title, Version: version}
	if title == "" || version == "" {
		return v, fmt.Errorf("id_token has no nintendo.ai/nintendo.av")
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	u := strings.TrimRight(baseURL, "/") + "/internal/title-version?title=" + url.QueryEscape(title) +
		"&version=" + url.QueryEscape(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return v, err
	}
	req.Header.Set("X-Internal-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, fmt.Errorf("nx-baas answered %d", resp.StatusCode)
	}
	var body struct {
		Latest         string `json:"latest"`
		UpdateRequired bool   `json:"update_required"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return v, fmt.Errorf("nx-baas answer: %w", err)
	}
	v.Latest, v.UpdateRequired = body.Latest, body.UpdateRequired
	return v, nil
}

// outdated remembers the verdict per battlenet subject, so the D2R-services
// connection (which only carries "openpak:<subject>") can find it.
var outdated sync.Map // subject -> versionVerdict

// recordLoginVersion runs the check for a login and logs the decision. It is
// called in its own goroutine: the authenticated Connect's schedule does not
// wait on nx-baas.
func recordLoginVersion(remote, subject, idToken string) {
	v, err := checkTitleVersion(context.Background(),
		env("NX_INTERNAL_URL", "http://openpak-nx-baas:20070"), env("NX_INTERNAL_KEY", ""), idToken)
	if err != nil {
		log.Printf("VERSION %s subject %s: title=%q version=%q unchecked (%s); allowed", remote, redact.Tag(subject), v.Title, v.Version, redact.Err(err))
		outdated.Delete(subject)
		return
	}
	if !v.UpdateRequired {
		log.Printf("VERSION %s subject %s: title=%s version=%s latest=%q current; allowed", remote, redact.Tag(subject), v.Title, v.Version, v.Latest)
		outdated.Delete(subject)
		return
	}
	outdated.Store(subject, v)
	log.Printf("VERSION %s subject %s: title=%s version=%s latest=%s update required; would refuse online (login answered as usual, push=%v)",
		remote, redact.Tag(subject), v.Title, v.Version, v.Latest, gameUpdatePushEnabled())
}

func gameUpdatePushEnabled() bool { return env("BATTLENET_GAME_UPDATE_PUSH", "") != "" }

// gameUpdateRequiredBody encodes GameUpdateRequest with field 1 only: the
// client treats a non-empty field 1 as "update required". Field 5 (u64) has
// no known meaning yet and is left out.
func gameUpdateRequiredBody(v versionVerdict) []byte {
	msg := fmt.Sprintf("Update required: version %s installed, %s available", v.Version, v.Latest)
	return pbBytes(1, []byte(msg))
}

// authSessionSubject reads AuthSessionRequest field 1 (LoginResponse field 2
// handed back) and returns the subject it proves, "" when it does not verify.
func authSessionSubject(body []byte) string {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return ""
		}
		body = body[n:]
		if num == 1 && typ == protowire.BytesType {
			b, k := protowire.ConsumeBytes(body)
			if k < 0 {
				return ""
			}
			return d2rTokenSubject(string(b))
		}
		k := protowire.ConsumeFieldValue(num, typ, body)
		if k < 0 {
			return ""
		}
		body = body[k:]
	}
	return ""
}

// maybePushGameUpdate sends GameVersion.GameUpdateRequired after the client's
// SetGameVersion when the knob is on and this subject's login was outdated.
func maybePushGameUpdate(s *gatewaySession) {
	if !gameUpdatePushEnabled() || s.subject == "" {
		return
	}
	got, ok := outdated.Load(s.subject)
	if !ok {
		return
	}
	v := got.(versionVerdict)
	s.send(d2rPush(gameVersionHash, gameUpdateRequired, 4, gameUpdateRequiredBody(v)))
	log.Printf("D2RSVC %s: pushed GameVersion.GameUpdateRequired (version %s, latest %s)", s.remote, v.Version, v.Latest)
}
