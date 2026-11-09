package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// xAI's error text reaches the client before an account has a credential, when core cannot redact
// what a hostile or careless login server echoes: it must be bounded and free of token-like
// strings, and the OAuth error code kept.
func TestLoginErrorTextIsBounded(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	srv := xaitest.New(t)
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oauth2/token" {
			return false
		}
		body, _ := json.Marshal(map[string]string{"error": "invalid_request", "error_description": "rejected " + jwt + " " + strings.Repeat("filler ", 500)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
		return true
	}
	set, _ := readSettings(srv.Settings())
	_, err := deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *llm.Error", err)
	}
	if strings.Contains(e.Message, "eyJhbGci") {
		t.Errorf("the message carries the token: %.200q", e.Message)
	}
	if n := len([]rune(e.Message)); n > 400 {
		t.Errorf("the message is %d characters: %.120q...", n, e.Message)
	}
	if !strings.Contains(e.Message, "invalid_request") || e.ProviderCode != "invalid_request" {
		t.Errorf("the error code was lost: %q, provider code %q", e.Message, e.ProviderCode)
	}

	long, _ := json.Marshal(map[string]string{"error": strings.Repeat("a", 5000)})
	if e := loginFailure(400, long, http.Header{}); len(e.Message) > 400 || e.ProviderCode != "" {
		t.Errorf("a long error code is kept: %d characters, provider code %.20q", len(e.Message), e.ProviderCode)
	}
}
