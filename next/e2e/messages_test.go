package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A Messages request with a block Chat Completions cannot carry (redacted thinking) is translated
// with the block dropped under the default drop policy, and refused with Anthropic's error
// envelope before any upstream call under drop_policy "reject".
func TestMessagesDropPolicy(t *testing.T) {
	const body = `{"model":"gpt-test","max_tokens":64,"messages":[
		{"role":"user","content":"Hi"},
		{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"Hello."}]},
		{"role":"user","content":"Again."}]}`
	reply := func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up1","object":"chat.completion","created":1700000000,"model":"gpt-test-upstream",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`)
	}

	t.Run("report", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u)
		resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"type":"message"`) || !strings.Contains(string(raw), `"text":"Done."`) {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		bodies, _ := u.received()
		if len(bodies) != 1 || strings.Contains(string(bodies[0]), "opaque") || !strings.Contains(string(bodies[0]), "Again.") {
			t.Errorf("upstream got %q, want one request with the turns and without the redacted block", bodies)
		}
	})

	t.Run("reject", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u, func(cfg map[string]any) { cfg["drop_policy"] = "reject" })
		resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"type":"error"`) ||
			!strings.Contains(string(raw), `"invalid_request_error"`) || !strings.Contains(string(raw), "messages[1].content[0]") {
			t.Errorf("status %d: %s; want 400 with an Anthropic error naming messages[1].content[0]", resp.StatusCode, raw)
		}
		if bodies, _ := u.received(); len(bodies) != 0 {
			t.Errorf("upstream got %d requests, want none", len(bodies))
		}
	})
}
