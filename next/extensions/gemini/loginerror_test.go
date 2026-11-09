package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
)

// A service account's token endpoint can echo anything: the error reaches the client when the
// account is added, before core holds a credential to redact, so its text is bounded and free of
// token-like strings, and the OAuth error code kept.
func TestTokenEndpointErrorTextIsBounded(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "rejected " + jwt + " " + strings.Repeat("filler ", 500)})
	}))
	defer ts.Close()
	var raw map[string]any
	_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
	raw["token_uri"] = ts.URL + "/token"
	b, _ := json.Marshal(raw)
	sa, err := readServiceAccount(b)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetchToken(context.Background(), sa)
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
	if !strings.Contains(e.Message, "invalid_grant") || e.ProviderCode != "invalid_grant" {
		t.Errorf("the error code was lost: %q, provider code %q", e.Message, e.ProviderCode)
	}
}
