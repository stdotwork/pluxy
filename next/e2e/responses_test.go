package e2e

import (
	"context"
	"fmt"
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

// Error events a Responses upstream sends reach the client as the upstream sent them, so they
// must lose the account's credential on the way like every other extension error. The upstream
// echoes its key in an error event, in a response.failed event and in a failed unstreamed
// Response.
func TestResponsesUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		key := keyForms(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		created := `data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}` + "\n\n"
		switch {
		case strings.Contains(string(body), `"model":"error-event"`):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, created)
			fmt.Fprintf(w, `data: {"type":"error","code":"bad_key_%s","message":"Rejected Bearer %s in error-event","param":null}`+"\n\n", key, key)
		case strings.Contains(string(body), `"model":"failed-event"`):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, created)
			fmt.Fprintf(w, `data: {"type":"response.failed","response":{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[],"error":{"code":"bad_key_%s","message":"Rejected Bearer %s in failed-event"}}}`+"\n\n", key, key)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":1,"status":"failed","model":"m","output":[],"error":{"code":"bad_key_%s","message":"Rejected Bearer %s in failed-unstreamed"}}`, key, key)
		}
	})

	// Each case gets its own core: an upstream error blocks the only account for later requests.
	for _, model := range []string{"error-event", "failed-event", "failed-unstreamed"} {
		t.Run(model, func(t *testing.T) {
			base, logs := startCoreLogged(t, u, func(cfg map[string]any) {
				acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
				acct["settings"].(map[string]any)["wire_api"] = "responses"
			})
			stream := model != "failed-unstreamed"
			body := fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hi"}`, model, stream)
			resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(raw), "Rejected") {
				t.Errorf("the upstream's message did not reach the client (status %d): %s", resp.StatusCode, raw)
			}
			if holdsKey(string(raw)) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
			all := logs.waitFor(t, model)
			if holdsKey(all) {
				t.Errorf("core's log holds the account's key:\n%s", all)
			}
		})
	}
}

// The event that ends a reply counts toward core's 64 MiB limit on a non-streamed reply like
// every event before it. This is the reviewer's case: 60 MiB of text and then an 8 MiB final
// Response object, which a Responses client gets as the reply, came back as a 200.
func TestNonStreamingReplyCountsTheFinalEvent(t *testing.T) {
	u := newUpstream(t)
	u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`+"\n\n")
		chunk := strings.Repeat("x", 64<<10)
		for sent := 0; sent < 60<<20; sent += len(chunk) {
			fmt.Fprintf(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":%q}`+"\n\n", chunk)
		}
		fmt.Fprint(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`+"\n\n")
		final := strings.Repeat("y", 8<<20)
		fmt.Fprintf(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}}`+"\n\n", final)
	})
	base := startCore(t, u, func(cfg map[string]any) {
		acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
		acct["settings"].(map[string]any)["wire_api"] = "responses"
	})
	resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, `{"model":"gpt-test","input":"hi"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "64 MiB") {
		t.Fatalf("status %d, want 502 naming the 64 MiB limit; body starts %.200s", resp.StatusCode, raw)
	}
}

// The ids a Responses reply gives its items count toward core's 64 MiB limit on a non-streamed
// reply: each message and reasoning item's id holds the upstream's response id. This is the
// reviewer's case: a 1 MiB response id and 65 items of each kind came back as a 200 of
// 69,213,758 bytes.
func TestNonStreamingResponsesCountsItemIDs(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		head := fmt.Sprintf(`"id":%q,"object":"chat.completion.chunk","created":1,"model":"m"`, strings.Repeat("i", 1<<20))
		write := func(delta string) bool {
			_, err := fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", head, delta)
			w.(http.Flusher).Flush()
			return err == nil && r.Context().Err() == nil
		}
		for range 65 {
			if !write(`{"reasoning_content":"r"}`) || !write(`{"content":"t"}`) {
				return
			}
		}
		fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", head)
	}
	base := startCore(t, u)
	resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, `{"model":"gpt-test","input":"hi"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "64 MiB") {
		t.Fatalf("status %d, want 502 naming the 64 MiB limit; body starts %.200s", resp.StatusCode, raw)
	}
}
