package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
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
