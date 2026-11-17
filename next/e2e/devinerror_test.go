package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// devinErrorBodyLimit is how much of a refusal's body the Devin extension reads.
const devinErrorBodyLimit = 1 << 20

// devinCutSecret is the account's session token in the cut-body case. The fake echoes it so that
// the read limit falls inside it: devinCutSecret[:devinCutAt] is read, the rest is not.
const (
	devinCutSecret = "devin-e2e-cut-secret-7f3a9c1d5e8b2a4f6c0d9e1b3a5c7f9e"
	devinCutAt     = 30
)

// devinChatError starts core with one Devin account whose upstream refuses every chat with a 500
// and body, sends one chat, and returns the client's status, its body and core's log.
func devinChatError(t *testing.T, token, body string) (int, string, *coreLog) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	base, logs := runCoreLogged(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "devin", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "devin-1", "models": []string{"m-devin"},
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": token},
			}},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"m-devin","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(got), logs
}

// An upstream refusal whose body is longer than the extension reads is cut. Core removes only
// whole copies of a credential, so if the upstream echoes the session token and the cut falls
// inside it, the cut text must not become the error: the token's start would reach the client
// and core's log in clear.
func TestDevinCutErrorBodyKeepsNoCredential(t *testing.T) {
	// Text, not JSON, so the extension can't take a message out of it; the token straddles the
	// read limit.
	head := "upstream failed for Authorization: Basic "
	filler := strings.Repeat("x", devinErrorBodyLimit-devinCutAt-len(head))
	body := filler + head + devinCutSecret + " and more text after it"
	if i := strings.Index(body, devinCutSecret); i+devinCutAt != devinErrorBodyLimit {
		t.Fatalf("the token starts at %d; the limit cuts it after %d bytes, want %d", i, devinErrorBodyLimit-i, devinCutAt)
	}
	status, got, logs := devinChatError(t, devinCutSecret, body)
	if status != http.StatusInternalServerError && status != http.StatusBadGateway {
		t.Errorf("status %d, want the upstream's refusal", status)
	}
	// The read part of the token, less a few bytes so a looser cut is caught too.
	start := devinCutSecret[:devinCutAt-6]
	if strings.Contains(got, start) {
		i := strings.Index(got, start)
		t.Errorf("the client's error body carries the token's start: %q", got[max(0, i-20):min(len(got), i+devinCutAt+20)])
	}
	if logs.contains(start) {
		t.Errorf("core's log carries the token's start")
	}
	if !strings.Contains(got, "500") {
		t.Errorf("the client's error does not say what the upstream answered: %.300s", got)
	}
}

// A short text refusal, read whole, still reaches the client as the error message.
func TestDevinShortTextErrorReachesClient(t *testing.T) {
	const text = "service overloaded, try again later"
	status, got, _ := devinChatError(t, "devin-e2e-short-token-0123456789", "  "+text+"\n")
	if status != http.StatusInternalServerError && status != http.StatusBadGateway {
		t.Errorf("status %d, want the upstream's refusal", status)
	}
	if !strings.Contains(got, `"`+text+`"`) {
		t.Errorf("the client's error body %q does not carry the upstream's text as the message", got)
	}
}
