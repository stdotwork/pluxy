package e2e

// Bug-class tro-codex-websocket-bridge-bugs: what a Codex client over the Responses WebSocket
// gets from core when the account's upstream holds no socket state, so core merges the
// transcript into every turn. The tests follow the public CLIProxyAPI issues the scan class
// lists: duplicate input[].id after the merge (share#3603) and a prewarm (generate: false)
// reaching the upstream (#1901).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// verifyCodexWSUpstream is a fake Codex upstream at /v1/responses. It answers each POST with a
// streamed Responses reply and keeps every request body it received. The first reply is a
// function_call item with the id fc-dup and the call id call-1, as an upstream's tool call; later
// replies are plain assistant messages.
type verifyCodexWSUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newVerifyCodexWSUpstream(t *testing.T) *verifyCodexWSUpstream {
	t.Helper()
	u := &verifyCodexWSUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		n := len(u.bodies)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range verifyCodexWSReply(n) {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// verifyCodexWSReply returns the stream events of the nth reply.
func verifyCodexWSReply(n int) [][]byte {
	id := fmt.Sprintf("resp_%d", n)
	var item map[string]any
	if n == 1 {
		item = map[string]any{"id": "fc-dup", "type": "function_call", "status": "completed",
			"call_id": "call-1", "name": "tool", "arguments": "{}"}
	} else {
		item = map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("reply %d", n), "annotations": []any{}}}}
	}
	resp := func(status string, output []any) map[string]any {
		return map[string]any{"id": id, "object": "response", "status": status, "model": "gpt-test", "output": output}
	}
	var out [][]byte
	for _, ev := range []map[string]any{
		{"type": "response.created", "sequence_number": 0, "response": resp("in_progress", []any{})},
		{"type": "response.output_item.done", "sequence_number": 1, "output_index": 0, "item": item},
		{"type": "response.completed", "sequence_number": 2, "response": resp("completed", []any{item})},
	} {
		b, _ := json.Marshal(ev)
		out = append(out, b)
	}
	return out
}

// received returns the request bodies the upstream got so far.
func (u *verifyCodexWSUpstream) received() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.bodies...)
}

// verifyCodexWSStart runs core with one Codex account on u, whose upstream WebSocket is off:
// core sends each of the socket's turns as an HTTP request carrying the whole conversation.
func verifyCodexWSStart(t *testing.T, u *verifyCodexWSUpstream) string {
	t.Helper()
	return runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-a",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": false},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-a"},
			}},
		},
	})
}

// verifyCodexWSMessage is a user message item with an id of its own.
func verifyCodexWSMessage(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": id, "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}}}
}

// verifyCodexWSRequest returns what the upstream got as input items.
func verifyCodexWSRequest(t *testing.T, body []byte) (input []map[string]any, fields map[string]json.RawMessage) {
	t.Helper()
	var req struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("upstream request %s: %v", body, err)
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("upstream request %s: %v", body, err)
	}
	return req.Input, fields
}

// verifyCodexWSInputTexts returns the input_text of every user message in input.
func verifyCodexWSInputTexts(input []map[string]any) []string {
	var texts []string
	for _, item := range input {
		content, _ := item["content"].([]any)
		for _, p := range content {
			part, _ := p.(map[string]any)
			if part["type"] == "input_text" {
				texts = append(texts, fmt.Sprint(part["text"]))
			}
		}
	}
	return texts
}

// The client sends a function_call with the previous response's item id (fc-dup) but a new call
// id (call-2), answered by its output; core merges that into the previous turn and the previous
// response's output, which holds fc-dup with call-1. The merged request must carry every
// input[].id once, keeping the copy the tool output answers: the Codex endpoint refuses the
// request otherwise ("Duplicate item found with id fc-...").
func TestVerifyCodexWebSocketMergeKeepsItemIDsUnique(t *testing.T) {
	u := newVerifyCodexWSUpstream(t)
	base := verifyCodexWSStart(t, u)

	c := dialResponses(t, base)
	first := wsTurn(t, c, map[string]any{"type": "response.create", "model": "gpt-test",
		"input": []any{verifyCodexWSMessage("msg-1", "one")}})
	if first["type"] != "response.completed" {
		t.Fatalf("first turn ended with %v", first)
	}
	if bodies := u.received(); len(bodies) != 1 {
		t.Fatalf("upstream got %d requests after the first turn, want 1", len(bodies))
	}

	second := wsTurn(t, c, map[string]any{"type": "response.create", "model": "gpt-test",
		"previous_response_id": responseID(first),
		"input": []any{
			map[string]any{"type": "function_call", "id": "fc-dup", "call_id": "call-2", "name": "tool", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "id": "out-1", "call_id": "call-2", "output": "ok"},
		}})
	if second["type"] != "response.completed" {
		t.Fatalf("second turn ended with %v", second)
	}

	bodies := u.received()
	if len(bodies) != 2 {
		t.Fatalf("upstream got %d requests, want 2", len(bodies))
	}
	input, fields := verifyCodexWSRequest(t, bodies[1])
	if _, ok := fields["previous_response_id"]; ok {
		t.Errorf("the merged request names previous_response_id: %s", bodies[1])
	}
	counts := map[string]int{}
	for _, item := range input {
		if id, _ := item["id"].(string); id != "" {
			counts[id]++
		}
	}
	for id, n := range counts {
		if n != 1 {
			t.Fatalf("upstream input carries id %q %d times, want once: %s", id, n, bodies[1])
		}
	}
	if counts["msg-1"] != 1 || counts["fc-dup"] != 1 || counts["out-1"] != 1 {
		t.Fatalf("upstream input ids are %v, want msg-1, fc-dup and out-1 once each: %s", counts, bodies[1])
	}
	var keptCall, keptOutput bool
	for _, item := range input {
		switch item["id"] {
		case "fc-dup":
			keptCall = item["type"] == "function_call" && item["call_id"] == "call-2"
		case "out-1":
			keptOutput = item["type"] == "function_call_output" && item["call_id"] == "call-2" && item["output"] == "ok"
		}
	}
	if !keptCall || !keptOutput {
		t.Fatalf("the kept call and output are not the answered pair call-2: %s", bodies[1])
	}
	if texts := verifyCodexWSInputTexts(input); len(texts) != 1 || texts[0] != "one" {
		t.Errorf("upstream input's user texts are %q, want the conversation's one message: %s", texts, bodies[1])
	}
}

// Codex CLI warms a new socket up with a response.create carrying generate: false, then
// continues the response id it got. The prewarm must cost no upstream request (the Codex client
// sends it on every connection, and forwarding it spends a full turn or, on an endpoint without
// generate, fails the prewarm), and the turn that continues the prewarm must still take the
// prewarm's input with it, since that input never reached the upstream.
func TestVerifyCodexWebSocketWarmupCostsNoUpstreamRequest(t *testing.T) {
	u := newVerifyCodexWSUpstream(t)
	base := verifyCodexWSStart(t, u)

	c := dialResponses(t, base)
	warmup := wsTurn(t, c, map[string]any{"type": "response.create", "model": "gpt-test", "generate": false,
		"input": []any{verifyCodexWSMessage("msg-1", "one")}})
	if warmup["type"] != "response.completed" {
		t.Fatalf("the warmup ended with %v", warmup)
	}
	id := responseID(warmup)
	if id == "" {
		t.Fatalf("the warmup answered %v, want a response id to continue", warmup)
	}
	if bodies := u.received(); len(bodies) != 0 {
		t.Fatalf("the warmup cost %d upstream requests, want none: %s", len(bodies), bodies)
	}

	turn := wsTurn(t, c, map[string]any{"type": "response.create", "model": "gpt-test",
		"previous_response_id": id,
		"input":                []any{verifyCodexWSMessage("msg-2", "two")}})
	if turn["type"] != "response.completed" {
		t.Fatalf("the turn continuing the warmup ended with %v", turn)
	}
	bodies := u.received()
	if len(bodies) != 1 {
		t.Fatalf("upstream got %d requests, want 1 (the turn, not the warmup)", len(bodies))
	}
	input, fields := verifyCodexWSRequest(t, bodies[0])
	if _, ok := fields["generate"]; ok {
		t.Errorf("the upstream request carries generate: %s", bodies[0])
	}
	if texts := verifyCodexWSInputTexts(input); len(texts) != 2 || texts[0] != "one" || texts[1] != "two" {
		t.Errorf("upstream input's user texts are %q, want the warmup's one then the turn's two: %s", texts, bodies[0])
	}
}
