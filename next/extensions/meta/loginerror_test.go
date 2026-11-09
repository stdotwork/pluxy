package main

import (
	"encoding/json"
	"errors"
	"net/http"
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
	for _, status := range []int{400, 401, 429, 503} {
		body, _ := json.Marshal(map[string]string{"error": hostileCode, "error_description": hostileText()})
		checkLoginError(t, "oauth error", loginFailure(status, body, http.Header{}), true)
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": hostileCode, "message": hostileText()}})
	checkLoginError(t, "api error", loginFailure(400, body, http.Header{}), true)
	long, _ := json.Marshal(map[string]string{"error": strings.Repeat("a", 5000)})
	checkLoginError(t, "long code", loginFailure(400, long, http.Header{}), false)
}
