package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// The provider's error text reaches the client before an account has a credential, when core
// cannot redact what a hostile or careless login server echoes: it must be bounded and free of
// token-like strings, and the OAuth error code kept.
const (
	hostileJWT  = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	hostileCode = "invalid_request"
)

func hostileText() string { return "rejected " + hostileJWT + " " + strings.Repeat("filler ", 500) }

func checkLoginError(t *testing.T, what string, err error, wantCode bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("%s: err = %v, want an *llm.Error", what, err)
	}
	if strings.Contains(e.Message, hostileJWT) || strings.Contains(e.Message, "eyJhbGci") {
		t.Errorf("%s: the message carries the token: %.200q", what, e.Message)
	}
	if n := len([]rune(e.Message)); n > 400 {
		t.Errorf("%s: the message is %d characters: %.120q...", what, n, e.Message)
	}
	if wantCode && !strings.Contains(e.Message, hostileCode) {
		t.Errorf("%s: the message lost the error code: %q", what, e.Message)
	}
}

func TestLoginErrorTextIsBounded(t *testing.T) {
	long, _ := json.Marshal(map[string]string{"error": strings.Repeat("a", 5000)})
	checkLoginError(t, "long code", loginFailure(400, long), false)
	withJWT, _ := json.Marshal(map[string]any{"error": map[string]string{"code": hostileJWT}})
	checkLoginError(t, "token as code", loginFailure(400, withJWT), false)
}

// The error a browser callback carries reaches the client before any credential exists: it is
// bounded and cleaned too.
func TestCallbackErrorTextIsBounded(t *testing.T) {
	_, _, err := signInResult(url.Values{"state": {"s"}, "error": {hostileCode + ": " + hostileText()}}, "s")
	checkLoginError(t, "callback error", err, true)
}
