package redact

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestTagHidesTheValue(t *testing.T) {
	for _, v := range []any{uint64(1234567890), "Some Nickname", "tok-abc123secret"} {
		line := fmt.Sprintf("session %s", Tag(v))
		if strings.Contains(line, fmt.Sprint(v)) {
			t.Fatalf("%v leaked into %q", v, line)
		}
		if Tag(v) != Tag(v) {
			t.Fatalf("tag for %v is not stable", v)
		}
	}
	if Tag(1) == Tag(2) {
		t.Fatal("two values share a tag")
	}
}

func TestErrDropsAddressesAndURLs(t *testing.T) {
	op := &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5000}, Err: errors.New("i/o timeout")}
	ue := &url.Error{Op: "Get", URL: "https://example.test/?token=secret-token", Err: op}
	for _, err := range []error{op, ue} {
		got := Err(err)
		if strings.Contains(got, "203.0.113.7") || strings.Contains(got, "secret-token") || !strings.Contains(got, "i/o timeout") {
			t.Fatalf("Err = %q", got)
		}
	}
}
