package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// An upstream that echoes the account's key in its error text must not get the key to the
// client or into core's log, whether the error comes before the reply or halfway through it, and
// whether or not the upstream escaped it in JSON (keyForms).
func TestUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		key := keyForms(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if strings.Contains(string(body), `"model":"refused"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"Rejected Bearer %s","type":"invalid_request_error","code":"bad_key_%s"}}`, key, key)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprintf(w, `data: {"error":{"message":"Rejected Bearer %s mid-stream","code":"revoked"}}`+"\n\n", key)
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
			if holdsKey(string(raw)) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
			all := logs.waitFor(t, "Rejected")
			if holdsKey(all) {
				t.Errorf("core's log holds the account's key:\n%s", all)
			}
		})
	}
}

// Upstream fields that reach the client as the upstream sent them, in a successful reply, must
// lose the account's key too, escaped or not: a Chat reply's system_fingerprint, written back to
// Chat clients, and a Gemini chunk, written back whole to Gemini clients.
func TestPassedThroughFieldsDoNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		echo := "Rejected " + keyForms(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+r.Header.Get("X-Goog-Api-Key"))
		switch {
		case r.URL.Path == "/v1/chat/completions" && strings.Contains(string(body), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"%s","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n", echo)
		case r.URL.Path == "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","system_fingerprint":"%s","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`, echo)
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"STOP","index":0}],"modelVersion":"%s"}`+"\n\n", echo)
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"STOP","index":0}],"modelVersion":"%s"}`, echo)
		default:
			http.NotFound(w, r)
		}
	})
	gemini := func(cfg map[string]any) {
		acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
		acct["settings"].(map[string]any)["wire_api"] = "gemini"
		acct["settings"].(map[string]any)["base_url"] = u.URL + "/v1beta"
	}
	chat := func(map[string]any) {}
	for _, c := range []struct {
		name, path, body string
		cfg              func(map[string]any)
	}{
		{"chat stream", "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, chat},
		{"chat unstreamed", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, chat},
		{"gemini stream", "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, gemini},
		{"gemini unstreamed", "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, gemini},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := startCoreLogged(t, u, c.cfg)
			resp := postPath(t, context.Background(), base, c.path, clientKey, c.body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Rejected") {
				t.Fatalf("the upstream's field did not reach the client (status %d): %s", resp.StatusCode, raw)
			}
			if holdsKey(string(raw)) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
		})
	}
}

// keyForms returns key as an upstream might echo it inside a JSON string, each form decoding back
// to key: as is, with its first character escaped (the reviewer's upstream), with one "-"
// escaped in upper-case hex, with every character escaped in alternating hex case, and inside
// JSON text with its first character escaped.
func keyForms(key string) string {
	var all strings.Builder
	for i, c := range key {
		if i%2 == 0 {
			fmt.Fprintf(&all, `\u%04x`, c)
		} else {
			fmt.Fprintf(&all, `\u%04X`, c)
		}
	}
	nested := `{\"key\":\"\\` + fmt.Sprintf(`u%04x`, key[0]) + key[1:] + `\"}` // JSON text holding the key escaped
	return strings.Join([]string{key, fmt.Sprintf(`\u%04x`, key[0]) + key[1:], strings.Replace(key, "-", `\u002D`, 1), all.String(), nested}, " ")
}

var jsonEscape = regexp.MustCompile(`\\(u[0-9a-fA-F]{4}|["\\/bfnrt])`)

// holdsKey reports whether s holds the account's key once its JSON escapes are decoded, layer
// after layer, as a client decoding the reply and any JSON inside it would read it.
func holdsKey(s string) bool {
	for range 8 {
		if strings.Contains(s, upstreamKey) {
			return true
		}
		s = jsonEscape.ReplaceAllStringFunc(s, func(m string) string {
			if m[1] == 'u' {
				n, _ := strconv.ParseUint(m[2:], 16, 32)
				return string(rune(n))
			}
			return m[1:]
		})
	}
	return strings.Contains(s, upstreamKey)
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
