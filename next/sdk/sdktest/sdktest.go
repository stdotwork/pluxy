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

func ClosedURL(t testing.TB) string {
	t.Helper()
	const addr = "127.0.0.1:1"
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatalf("something listens on %s; the connection-refused cases need it closed", addr)
	}
	return "http://" + addr
}

func HangUp(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

func LoginServer(t testing.TB, answer func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

const (
	HostileJWT  = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	HostileCode = "invalid_request"
)

func HostileText() string { return "rejected " + HostileJWT + " " + strings.Repeat("filler ", 500) }

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
