package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sessions, games, characters and the web tier must leave no account id,
// token, name, game name or address in the log.
func TestLogsCarryNoPersonalData(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(log.Writer())
	t.Setenv("BATTLENET_CHARACTER_STORE", t.TempDir()+"/characters.json")
	resetCharacters()

	const (
		addr    = "203.0.113.77:44312"
		subject = "7788991122"
		cname   = "Zorblaxa"
		gname   = "secretgamename"
		bearer  = "Bearer s3cr3t-bearer"
		query   = "q-token-9f8e7d"
	)
	token := d2rSessionToken(subject)
	s := signIn(t, addr, token)
	d2rCall(t, s, characterHash, charCreate,
		append(append(pbBytes(1, []byte(cname)), pbVarint(2, 4)...), pbVarint(3, 0)...))
	d2rCall(t, s, characterHash, charReadList, nil)
	createGameAs(t, s, gname, "", 8)
	d2rCall(t, s, gameManagementHash, gameDetail, pbBytes(1, []byte(gname)))
	d2rCall(t, s, gameManagementHash, gameJoin, append(pbVarint(1, 1), pbBytes(2, []byte(gname))...))

	// The web tier: headers, query and body are tokens in this protocol.
	req := httptest.NewRequest(http.MethodPost, "/login/check_token?access_token="+query, strings.NewReader(`{"token":"body-token-123"}`))
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Cookie", "sid=cookie-secret")
	dump(httptest.NewRecorder(), req, "ACCOUNT")

	got := out.String()
	if !strings.Contains(got, "D2RSVC ~") {
		t.Fatalf("session lines missing or untagged:\n%s", got)
	}
	for _, secret := range []string{
		subject, token, cname, gname, "203.0.113.77", "44312", "s3cr3t-bearer", "cookie-secret", query, "body-token-123",
	} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains %q:\n%s", secret, got)
		}
	}
}
