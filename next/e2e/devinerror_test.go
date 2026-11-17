package e2e

import (
	"context"
	"fmt"
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

// devinStatusMessage is the error's message when the extension passes on no text from the body.
const devinStatusMessage = `"Devin answered 500 Internal Server Error"`

// devinChatError starts core with one Devin account whose upstream answers every chat with
// answer, sends one chat, and returns the client's status, its body and core's log.
func devinChatError(t *testing.T, token string, answer http.HandlerFunc) (int, string, *coreLog) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		answer(w, r)
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

// textRefusal answers a 500 with body as plain text.
func textRefusal(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	}
}

// checkNoTokenPart fails the test if the client's error body or core's log holds part.
func checkNoTokenPart(t *testing.T, got string, logs *coreLog, part string) {
	t.Helper()
	if i := strings.Index(got, part); i >= 0 {
		t.Errorf("the client's error body carries part of the token: %q", got[max(0, i-40):min(len(got), i+len(part)+20)])
	}
	if logs.contains(part) {
		t.Errorf("core's log carries part of the token")
	}
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
	status, got, logs := devinChatError(t, devinCutSecret, textRefusal(body))
	if status != http.StatusInternalServerError {
		t.Errorf("status %d, want the upstream's 500", status)
	}
	// The read part of the token, less a few bytes so a looser cut is caught too.
	checkNoTokenPart(t, got, logs, devinCutSecret[:devinCutAt-6])
	if !strings.Contains(got, devinStatusMessage) {
		t.Errorf("the client's error does not say what the upstream answered: %.300s", got)
	}
}

// A refusal whose body breaks off before its end (the connection closes inside the declared
// length, or before a chunked body's last chunk) was not read whole: what arrived may end inside
// the token the upstream echoed. The error says only the status. The part read here is shorter
// than the token-shaped runs the text cleaner replaces, so only the read's error keeps it out.
func TestDevinBrokenErrorBodyKeepsNoCredential(t *testing.T) {
	const secret = "e2e-early-7f3a9c1d5e8b2a4f6c0d9e1b3a5c7f9e"
	const read = 20 // bytes of secret sent before the connection closes
	part := "rejected session token " + secret[:read]
	for _, tc := range []struct {
		name, head, body string
	}{
		{"content-length", "Content-Length: 100000\r\n", part},
		{"chunked", "Transfer-Encoding: chunked\r\n", fmt.Sprintf("%x\r\n%s\r\n", len(part), part)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, got, logs := devinChatError(t, secret, func(w http.ResponseWriter, _ *http.Request) {
				conn, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				_, _ = buf.WriteString("HTTP/1.1 500 Internal Server Error\r\nContent-Type: text/plain\r\n" + tc.head + "\r\n" + tc.body)
				_ = buf.Flush()
			})
			if status != http.StatusInternalServerError {
				t.Errorf("status %d, want the upstream's 500", status)
			}
			checkNoTokenPart(t, got, logs, secret[:read-4])
			if !strings.Contains(got, devinStatusMessage) {
				t.Errorf("the client's error does not say only the status: %.300s", got)
			}
		})
	}
}

// A text refusal read whole still holds part of a token the upstream echoed when the upstream
// split it across lines or cut it short itself; core, removing only whole copies, would pass the
// part on. The text is cleaned first: the parts are replaced, and the rest of the text stays.
func TestDevinTextErrorIsCleaned(t *testing.T) {
	const secret = "devin-e2e-zz-secret-7f3a9c1d5e8b2a4f6c0d9e1b3a5c7f9e"
	for _, tc := range []struct {
		name, body, want string
		parts            []string
	}{
		{"split across lines", "upstream failed for Authorization: Basic " + secret[:26] + "\n" + secret[26:] + "\n",
			`"upstream failed for Authorization: Basic [redacted] [redacted]"`, []string{secret[:20], secret[26:46]}},
		{"cut by the upstream", "invalid session token " + secret[:30] + "... (truncated)",
			`"invalid session token [redacted] (truncated)"`, []string{secret[:20]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, got, logs := devinChatError(t, secret, textRefusal(tc.body))
			if status != http.StatusInternalServerError {
				t.Errorf("status %d, want the upstream's 500", status)
			}
			for _, p := range tc.parts {
				checkNoTokenPart(t, got, logs, p)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("the client's error body %q does not carry the cleaned text %s", got, tc.want)
			}
		})
	}
}

// A short text refusal, read whole, still reaches the client as the error message.
func TestDevinShortTextErrorReachesClient(t *testing.T) {
	const text = "service overloaded, try again later"
	status, got, _ := devinChatError(t, "devin-e2e-short-token-0123456789", textRefusal("  "+text+"\n"))
	if status != http.StatusInternalServerError {
		t.Errorf("status %d, want the upstream's 500", status)
	}
	if !strings.Contains(got, `"`+text+`"`) {
		t.Errorf("the client's error body %q does not carry the upstream's text as the message", got)
	}
}
