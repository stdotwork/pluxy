package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An upstream that echoes the account's key in its error text must not get the key to the
// client or into core's log, whether the error comes before the reply or halfway through it.
func TestUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		auth := r.Header.Get("Authorization") // "Bearer <key>"
		if strings.Contains(string(body), `"model":"refused"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"Rejected %s","type":"invalid_request_error","code":"bad_key_%s"}}`, auth, auth[len("Bearer "):])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprintf(w, `data: {"error":{"message":"Rejected %s mid-stream","code":"revoked"}}`+"\n\n", auth)
	}

	// Each case gets its own core: routing blocks an account that the upstream refused, so a
	// shared core would answer the later cases itself without calling the upstream.
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"before reply", `{"model":"refused","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized},
		{"before reply, unstreamed", `{"model":"refused","messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized},
		{"mid-stream", `{"model":"midway","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, logs := startCoreLogged(t, u)
			resp := post(t, context.Background(), base, clientKey, c.body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, c.status, raw)
			}
			if !strings.Contains(string(raw), "Rejected") {
				t.Errorf("the upstream's message did not reach the client: %s", raw)
			}
			if strings.Contains(string(raw), upstreamKey) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
			all := logs.waitFor(t, "Rejected")
			if strings.Contains(all, upstreamKey) {
				t.Errorf("core's log holds the account's key:\n%s", all)
			}
		})
	}
}

// A request under the HTTP body limit must reach the upstream even when JSON would escape much
// of it ("<" becomes six bytes with HTML escaping), and it reaches it unchanged. This is the
// reviewer's case: a 6,291,511-byte body of "<" overflowed the 64 MiB extension frame.
func TestLargeRequestFitsTheFrame(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	base := startCore(t, u)
	head, tail := `{"model":"gpt-test","messages":[{"role":"user","content":"`, `"}]}`
	body := head + strings.Repeat("<", 6291511-len(head)-len(tail)) + tail
	resp := post(t, context.Background(), base, clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %.300s", resp.StatusCode, raw)
	}
	bodies, _ := u.received()
	if len(bodies) != 1 || string(bodies[0]) != body {
		t.Errorf("the upstream did not get the client's body unchanged")
	}
}

// A request under the HTTP body limit that still cannot fit one extension frame is the
// client's to fix: 413, not a server failure. U+2028 is three bytes in the body and six once
// escaped in the request's parsed copy.
func TestRequestTooLargeForTheFrameIsAClientError(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		t.Error("the upstream was called")
	}
	base := startCore(t, u)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("\u2028", 8_000_000) + `"}]}`
	if len(body) >= 24<<20 {
		t.Fatalf("test body is %d bytes, not under the HTTP limit", len(body))
	}
	resp := post(t, context.Background(), base, clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), `"type":"invalid_request_error"`) {
		t.Fatalf("status %d: %s; want 413 and an invalid_request_error", resp.StatusCode, raw)
	}
}
