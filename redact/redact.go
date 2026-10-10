// Package redact keeps personal data out of logs. A log line never carries a
// pid, account id, name, token, ticket or address; where two lines have to be
// matched up it carries a Tag instead.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// key is random per process: tags correlate lines within one run and cannot be
// reversed or compared across restarts.
var key = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}()

// Tag returns a short stable per-process tag for v ("~" and six hex digits).
func Tag(v any) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprint(m, v)
	return "~" + hex.EncodeToString(m.Sum(nil)[:3])
}

// Err returns err's text without the addresses and URLs that net and url
// errors spell out ("read tcp 10.0.0.1:443->203.0.113.7:5000: ...").
func Err(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return Err(ue.Err)
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Op + ": " + oe.Err.Error()
	}
	return err.Error()
}
