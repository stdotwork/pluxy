package e2e

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A Gemini request whose model turn holds a thought, which Chat Completions has no place for, is
// translated with the thought dropped under the default drop policy, and refused with Google's
// error envelope before any upstream call under drop_policy "reject". The thought signature on the
// call is carried over to the Chat upstream.
func TestGeminiDropPolicy(t *testing.T) {
	const body = `{"contents":[
		{"role":"user","parts":[{"text":"Hi"}]},
		{"role":"model","parts":[{"text":"secret thought","thought":true},{"functionCall":{"id":"c1","name":"f","args":{}},"thoughtSignature":"sig"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"f","response":{"content":{"ok":true}}}}]},
		{"role":"user","parts":[{"text":"Again."}]}]}`
	reply := func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up1","object":"chat.completion","created":1700000000,"model":"gpt-test-upstream",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`)
	}
	const path = "/v1beta/models/gpt-test:generateContent"

	t.Run("report", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u)
		resp := postPath(t, context.Background(), base, path, clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"text":"Done."`) || !strings.Contains(string(raw), `"finishReason":"STOP"`) {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		bodies, _ := u.received()
		if len(bodies) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(bodies))
		}
		up := string(bodies[0])
		if strings.Contains(up, "secret thought") || !strings.Contains(up, `"thought_signature":"sig"`) || !strings.Contains(up, "Again.") {
			t.Errorf("upstream got %s, want one request without the thought, with the thought signature and the turns", up)
		}
	})

	t.Run("reject", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u, func(cfg map[string]any) { cfg["drop_policy"] = "reject" })
		resp := postPath(t, context.Background(), base, path, clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"status":"INVALID_ARGUMENT"`) ||
			!strings.Contains(string(raw), "contents[1].parts[0]") {
			t.Errorf("status %d: %s; want 400 with a Google error naming contents[1].parts[0]", resp.StatusCode, raw)
		}
		if bodies, _ := u.received(); len(bodies) != 0 {
			t.Errorf("upstream got %d requests, want none", len(bodies))
		}
	})
}

// An upstream that redirects to another host does not get the account's key sent there: Go drops
// Authorization on such a redirect but not x-goog-api-key or x-api-key. The client gets a 502.
func TestUpstreamRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	_, port, _ := net.SplitHostPort(other.Listener.Addr().String())
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	base := startCore(t, &upstream{Server: redirect}, func(cfg map[string]any) {
		ext := cfg["extension"].(map[string]any)
		acct := ext["accounts"].([]any)[0].(map[string]any)
		acct["settings"] = map[string]any{"base_url": redirect.URL + "/v1beta", "wire_api": "gemini"}
	})
	resp := postPath(t, context.Background(), base, "/v1beta/models/gemini-test:generateContent", clientKey,
		`{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if n := leaked.Load(); n != 0 {
		t.Errorf("the other host got %d requests carrying the account's key", n)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d: %s; want 502", resp.StatusCode, raw)
	}
}
