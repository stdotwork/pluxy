// Package sdktest holds the test helpers that provider extensions' tests share: a refused
// connection, a server that hangs up, an assertion on the *llm.Error a call returned, and the
// checks that a login error's text is bounded and free of token-like strings. It imports
// testing, so only test files use it.
package sdktest

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// WantError fails the test unless err is an *llm.Error with the given code and retryable flag.
func WantError(t testing.TB, err error, code string, retryable bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want an *llm.Error", err)
	}
	if e.Code != code || e.Retryable != retryable {
		t.Errorf("got code %s retryable %v, want code %s retryable %v (%s)", e.Code, e.Retryable, code, retryable, e.Message)
	}
}

// ClosedURL returns the address of a port nothing listens on: port 1 on loopback, which no test
// server can be given, unlike a just-closed ephemeral port that the next httptest server may reuse.
func ClosedURL(t testing.TB) string {
	t.Helper()
	const addr = "127.0.0.1:1"
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatalf("something listens on %s; the connection-refused cases need it closed", addr)
	}
	return "http://" + addr
}

// HangUp closes the connection of the request being answered without a reply.
func HangUp(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

// LoginServer is a server whose every reply is answer. It returns the server's URL and closes
// the server when the test ends.
func LoginServer(t testing.TB, answer func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The provider's error text reaches the client before an account has a credential, when core
// cannot redact what a hostile or careless login server echoes: it must be bounded and free of
// token-like strings, and the OAuth error code kept.
const (
	HostileJWT  = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	HostileCode = "invalid_request"
)

// HostileText is login-server text a careless provider might echo: a token-like string, then a
// long run of filler.
func HostileText() string { return "rejected " + HostileJWT + " " + strings.Repeat("filler ", 500) }

// CheckLoginError fails the test unless err is an *llm.Error whose message is bounded and carries
// no token-like string, and, when wantCode, still names HostileCode. what says which case it is.
func CheckLoginError(t testing.TB, what string, err error, wantCode bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("%s: err = %v, want an *llm.Error", what, err)
	}
	if strings.Contains(e.Message, HostileJWT) || strings.Contains(e.Message, "eyJhbGci") {
		t.Errorf("%s: the message carries the token: %.200q", what, e.Message)
	}
	if n := len([]rune(e.Message)); n > 400 {
		t.Errorf("%s: the message is %d characters: %.120q...", what, n, e.Message)
	}
	if wantCode && !strings.Contains(e.Message, HostileCode) {
		t.Errorf("%s: the message lost the error code: %q", what, e.Message)
	}
}
