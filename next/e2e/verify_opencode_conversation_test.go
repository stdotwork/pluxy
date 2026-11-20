package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type verifySSEEvent struct {
	name string
	data map[string]any
}

func verifyPost(t *testing.T, ctx context.Context, base, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientKey)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// verifyReadSSE reads a complete event stream as an Anthropic SDK does. It keeps the event name
// and the JSON data so a test can talk about both the stream's ordering and its fields.
func verifyReadSSE(t *testing.T, body io.Reader) []verifySSEEvent {
	t.Helper()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	var events []verifySSEEvent
	var name string
	var data []string
	flush := func() {
		if name == "" && len(data) == 0 {
			return
		}
		e := verifySSEEvent{name: name}
		if len(data) > 0 {
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e.data); err != nil {
				t.Fatalf("event %q has invalid JSON: %v", name, err)
			}
		}
		events = append(events, e)
		name, data = "", nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if s, ok := strings.CutPrefix(line, "event: "); ok {
			name = s
			continue
		}
		if s, ok := strings.CutPrefix(line, "data: "); ok {
			data = append(data, s)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	flush()
	return events
}

func verifyFindEvent(events []verifySSEEvent, typ string, pred func(map[string]any) bool) int {
	for i, ev := range events {
		if ev.name == typ && (pred == nil || pred(ev.data)) {
			return i
		}
	}
	return -1
}

func verifyText(events []verifySSEEvent) string {
	var text strings.Builder
	for _, ev := range events {
		if ev.name != "content_block_delta" {
			continue
		}
		delta, _ := ev.data["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			s, _ := delta["text"].(string)
			text.WriteString(s)
		}
	}
	return text.String()
}

// OpenCode's main turn sends a Messages stream with tools, executes the tool, and sends the
// result back in the next turn. A reply must end the tool_use block and say stop_reason
// tool_use so OpenCode runs the tool, and the result must reach a Chat upstream as a tool call
// and its tool result rather than disappearing or ending the conversation.
func TestVerifyOpenCodeStreamedToolConversation(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		write := func(s string) {
			fmt.Fprintf(w, "data: %s\n\n", s)
			f.Flush()
		}
		const head = `"id":"chatcmpl-opencode","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream"`
		if !bytes.Contains(body, []byte(`"tool_call_id":"call_readme"`)) {
			write(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
			write(`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_readme","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`)
			write(`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":null}]}`)
			write(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			write(`[DONE]`)
			return
		}
		write(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
		write(`{` + head + `,"choices":[{"index":0,"delta":{"content":"The file has 3 lines."},"finish_reason":null}]}`)
		write(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`[DONE]`)
	}
	base := startCore(t, u)

	headers := map[string]string{
		"x-opencode-session-id": "ses_tool_turn",
		"x-session-affinity":    "ses_tool_turn",
		"X-Session-Id":          "ses_tool_turn",
		"x-opencode-project":    "project-one",
		"x-opencode-session":    "ses_tool_turn",
		"x-opencode-client":     "opencode",
	}
	const firstBody = `{"model":"gpt-test","max_tokens":128,"stream":true,
		"tools":[{"name":"read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
		"messages":[{"role":"user","content":"Read README.md and tell me how many lines it has."}]}`
	resp := verifyPost(t, context.Background(), base, "/v1/messages", headers, firstBody)
	events := verifyReadSSE(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first turn status %d: %+v", resp.StatusCode, events)
	}
	start := verifyFindEvent(events, "content_block_start", func(data map[string]any) bool {
		block, _ := data["content_block"].(map[string]any)
		return block["type"] == "tool_use"
	})
	stop := verifyFindEvent(events, "content_block_stop", nil)
	finish := verifyFindEvent(events, "message_delta", func(data map[string]any) bool {
		delta, _ := data["delta"].(map[string]any)
		return delta["stop_reason"] == "tool_use"
	})
	end := verifyFindEvent(events, "message_stop", nil)
	if start < 0 || stop <= start || finish < stop || end < finish {
		t.Fatalf("tool stream events = %+v; want a tool_use block, its stop, stop_reason tool_use and message_stop", events)
	}
	block, _ := events[start].data["content_block"].(map[string]any)
	if block["id"] != "call_readme" || block["name"] != "read" {
		t.Fatalf("tool_use block = %+v, want the upstream call id and name", block)
	}
	var arguments strings.Builder
	for _, ev := range events[start+1 : stop] {
		if ev.name != "content_block_delta" {
			continue
		}
		delta, _ := ev.data["delta"].(map[string]any)
		if delta["type"] == "input_json_delta" {
			s, _ := delta["partial_json"].(string)
			arguments.WriteString(s)
		}
	}
	var input map[string]string
	if err := json.Unmarshal([]byte(arguments.String()), &input); err != nil || input["path"] != "README.md" {
		t.Fatalf("tool input = %q (%v), want {\"path\":\"README.md\"}", arguments.String(), err)
	}

	const secondBody = `{"model":"gpt-test","max_tokens":128,"stream":true,
		"tools":[{"name":"read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
		"messages":[
			{"role":"user","content":"Read README.md and tell me how many lines it has."},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_readme","name":"read","input":{"path":"README.md"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_readme","content":"one\ntwo\nthree\n"}]}
		]}`
	resp = verifyPost(t, context.Background(), base, "/v1/messages", headers, secondBody)
	events = verifyReadSSE(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second turn status %d: %+v", resp.StatusCode, events)
	}
	if got := verifyText(events); got != "The file has 3 lines." {
		t.Fatalf("second turn text = %q, want the upstream's final answer", got)
	}
	if verifyFindEvent(events, "message_stop", nil) < 0 {
		t.Fatalf("second turn stream has no message_stop: %+v", events)
	}

	bodies, _ := u.received()
	if len(bodies) != 2 {
		t.Fatalf("upstream got %d turns, want 2", len(bodies))
	}
	var sent struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodies[1], &sent); err != nil {
		t.Fatalf("second upstream body is not JSON: %v\n%s", err, bodies[1])
	}
	var gotCall, gotResult bool
	for _, msg := range sent.Messages {
		if len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call_readme" && msg.ToolCalls[0].Function.Name == "read" {
			var args map[string]string
			if json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &args) == nil && args["path"] == "README.md" {
				gotCall = true
			}
		}
		if msg.Role == "tool" && msg.ToolCallID == "call_readme" {
			var output string
			if json.Unmarshal(msg.Content, &output) == nil && output == "one\ntwo\nthree\n" {
				gotResult = true
			}
		}
	}
	if !gotCall || !gotResult {
		t.Fatalf("second upstream messages = %s; want the read call and its tool result", bodies[1])
	}
}

// OpenCode names a session in its headers, then asks for a title with a small streaming Messages
// request. The title comes from the stream's text; without its terminal events OpenCode leaves
// the timestamp placeholder. The headers also carry the parent session on subagent turns, so
// title and subagent requests can share the main turn's cache and account.
func TestVerifyOpenCodeTitleStream(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		write := func(s string) {
			fmt.Fprintf(w, "data: %s\n\n", s)
			f.Flush()
		}
		const head = `"id":"chatcmpl-title","object":"chat.completion.chunk","created":1700000001,"model":"gpt-test-upstream"`
		write(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
		write(`{` + head + `,"choices":[{"index":0,"delta":{"content":"Friendly greeting"},"finish_reason":null}]}`)
		write(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`[DONE]`)
	}
	base := startCore(t, u)
	headers := map[string]string{
		"x-opencode-session-id": "ses_title",
		"x-session-affinity":    "ses_title",
		"X-Session-Id":          "ses_title",
		"x-opencode-project":    "project-one",
		"x-opencode-session":    "ses_title",
		"x-opencode-client":     "opencode",
	}
	const body = `{"model":"gpt-test","max_tokens":32,"stream":true,
		"system":[{"type":"text","text":"You are a title generator."}],
		"messages":[{"role":"user","content":"Help me debug the failing build"}]}`
	resp := verifyPost(t, context.Background(), base, "/v1/messages", headers, body)
	events := verifyReadSSE(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("title status %d: %+v", resp.StatusCode, events)
	}
	if got := verifyText(events); got != "Friendly greeting" {
		t.Fatalf("title text = %q, want a non-empty title", got)
	}
	if verifyFindEvent(events, "message_delta", func(data map[string]any) bool {
		delta, _ := data["delta"].(map[string]any)
		return delta["stop_reason"] == "end_turn"
	}) < 0 {
		t.Fatalf("title stream has no end_turn: %+v", events)
	}
	if verifyFindEvent(events, "message_stop", nil) < 0 {
		t.Fatalf("title stream has no message_stop: %+v", events)
	}
}

// A child OpenCode session sends its own x-opencode-session-id but names its parent in
// X-Session-Id, x-session-affinity and x-opencode-parent-session-id. With session affinity on,
// the child must use the parent's account, as the main turn did, rather than round-robin to the
// next account and split the conversation's cache.
func TestVerifyOpenCodeSubagentKeepsParentAffinity(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	base := startCoreAccounts(t, u, map[string]any{"session_affinity": map[string]any{}}, "sk-a", "sk-b")
	const body = `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"Find the callers."}]}`
	parent := map[string]string{
		"x-opencode-session-id": "ses_parent",
		"x-session-affinity":    "ses_parent",
		"X-Session-Id":          "ses_parent",
		"x-opencode-project":    "project-one",
		"x-opencode-session":    "ses_parent",
		"x-opencode-client":     "opencode",
	}
	child := map[string]string{
		"x-opencode-session-id":        "ses_child",
		"x-opencode-parent-session-id": "ses_parent",
		"x-session-affinity":           "ses_parent",
		"X-Session-Id":                 "ses_parent",
		"x-parent-session-id":          "ses_parent",
		"x-opencode-project":           "project-one",
		"x-opencode-session":           "ses_parent",
		"x-opencode-client":            "opencode",
	}
	for _, headers := range []struct {
		name    string
		headers map[string]string
	}{{"parent", parent}, {"subagent", child}} {
		resp := verifyPost(t, context.Background(), base, "/v1/messages", headers.headers, body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d: %s", headers.name, resp.StatusCode, raw)
		}
	}
	if got := usedKeys(u); len(got) != 2 || got[0] != "sk-a" || got[1] != got[0] {
		t.Fatalf("upstream keys = %v, want the parent and subagent on [sk-a sk-a]", got)
	}
}
