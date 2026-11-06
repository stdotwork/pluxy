package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A Responses request with a part Chat Completions cannot carry (a file by URL) is translated
// with the part dropped under the default drop policy, and refused before any upstream call
// under drop_policy "reject".
func TestResponsesDropPolicy(t *testing.T) {
	const body = `{"model":"gpt-test","input":[{"role":"user","content":[
		{"type":"input_text","text":"Summarise this."},
		{"type":"input_file","file_url":"https://example.com/report.pdf"}]}]}`
	reply := func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up1","object":"chat.completion","created":1700000000,"model":"gpt-test-upstream",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`)
	}

	t.Run("report", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u)
		resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"text":"Done."`) {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		bodies, _ := u.received()
		if len(bodies) != 1 || strings.Contains(string(bodies[0]), "report.pdf") || !strings.Contains(string(bodies[0]), "Summarise this.") {
			t.Errorf("upstream got %q, want one request with the text and without the file", bodies)
		}
	})

	t.Run("reject", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u, func(cfg map[string]any) { cfg["drop_policy"] = "reject" })
		resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "input[0].content[1]") {
			t.Errorf("status %d: %s; want 400 naming input[0].content[1]", resp.StatusCode, raw)
		}
		if bodies, _ := u.received(); len(bodies) != 0 {
			t.Errorf("upstream got %d requests, want none", len(bodies))
		}
	})
}
