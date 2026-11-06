package e2e

import (
	"context"
	"fmt"
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

// Errors a Messages upstream sends reach a Messages client as the upstream sent them, so they
// must lose the account's credential on the way like every other extension error. The upstream
// echoes its key in a mid-stream error event and in an unstreamed error body.
func TestMessagesUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		key := r.Header.Get("X-Api-Key")
		if strings.Contains(string(body), `"model":"error-event"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
			fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"Rejected %s in error-event\"}}\n\n", key)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":"Rejected %s in error-body"}}`, key)
	})

	// Each case gets its own core: an upstream error blocks the only account for later requests.
	for _, c := range []struct{ model, body string }{
		{"error-event", `{"model":"error-event","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"error-body", `{"model":"error-body","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(c.model, func(t *testing.T) {
			base, logs := startCoreLogged(t, u, func(cfg map[string]any) {
				acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
				acct["settings"].(map[string]any)["wire_api"] = "messages"
			})
			resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, c.body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(raw), "Rejected") {
				t.Errorf("the upstream's message did not reach the client (status %d): %s", resp.StatusCode, raw)
			}
			if strings.Contains(string(raw), upstreamKey) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
			all := logs.waitFor(t, "in "+c.model)
			if strings.Contains(all, upstreamKey) {
				t.Errorf("core's log holds the account's key:\n%s", all)
			}
		})
	}
}
