package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

// The shapes below are Anthropic's own, from the "Streaming messages" page
// (https://docs.claude.com/en/docs/build-with-claude/streaming): a message_start carrying a
// Message with an empty content array and numeric usage, one content_block_start (with a JSON
// object input for tool_use), any number of content_block_delta whose delta carries its typed
// field (text, thinking, partial_json or signature), a content_block_stop, a message_delta whose
// delta has a stop_reason string and whose usage has a numeric output_tokens, and a message_stop;
// a mid-stream error event carries {"type":"error","error":{"type":...,"message":...}}.

// verifyShapeEvent is one server-sent event of a Messages reply.
type verifyShapeEvent struct {
	name string
	data map[string]any
}

// verifyShapeEvents parses a Messages SSE body and checks each event's name against its data type.
func verifyShapeEvents(t *testing.T, raw string) []verifyShapeEvent {
	t.Helper()
	var events []verifyShapeEvent
	for _, block := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var name string
		var data []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
		if name == "" || len(data) == 0 {
			t.Errorf("SSE block has no event name or data: %q", block)
			continue
		}
		joined := strings.Join(data, "\n")
		var m map[string]any
		if err := json.Unmarshal([]byte(joined), &m); err != nil {
			t.Errorf("event %q: data is not a JSON object: %v: %s", name, err, joined)
			continue
		}
		if typ, _ := m["type"].(string); typ != name {
			t.Errorf("SSE event name %q but data type is %v", name, m["type"])
		}
		events = append(events, verifyShapeEvent{name: name, data: m})
	}
	return events
}

// verifyShapeObject returns the JSON object at key, reporting a missing, null or wrongly typed one.
func verifyShapeObject(t *testing.T, where string, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("%s: %q is missing", where, key)
		return nil
	}
	o, ok := v.(map[string]any)
	if !ok {
		t.Errorf("%s: %q is %T (%v), want a JSON object", where, key, v, v)
		return nil
	}
	return o
}

// verifyShapeNumber reports a value that is not a JSON number, or not a whole number when whole.
func verifyShapeNumber(t *testing.T, where string, v any, whole bool) {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Errorf("%s: %T (%v), want a number", where, v, v)
		return
	}
	if whole && f != math.Trunc(f) {
		t.Errorf("%s: %v, want a whole number", where, f)
	}
}

// verifyShapeString reports a field that is not a string.
func verifyShapeString(t *testing.T, where string, m map[string]any, key string, nonEmpty bool) {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Errorf("%s: %q is %T (%v), want a string", where, key, m[key], m[key])
		return
	}
	if nonEmpty && s == "" {
		t.Errorf("%s: %q is empty", where, key)
	}
}

// verifyShapeUsage checks a Messages usage object. output_tokens is what the streaming page shows
// on message_delta; input_tokens is shown on message_start. Both must be numbers when present.
func verifyShapeUsage(t *testing.T, where string, m map[string]any, wantInput bool) {
	t.Helper()
	u := verifyShapeObject(t, where, m, "usage")
	if u == nil {
		return
	}
	if _, ok := u["output_tokens"]; !ok {
		t.Errorf("%s.usage: output_tokens is missing", where)
	} else {
		verifyShapeNumber(t, where+".usage.output_tokens", u["output_tokens"], true)
	}
	if _, ok := u["input_tokens"]; ok {
		verifyShapeNumber(t, where+".usage.input_tokens", u["input_tokens"], true)
	} else if wantInput {
		t.Errorf("%s.usage: input_tokens is missing", where)
	}
}

// verifyShapeMessage checks a Message object as the streaming page shows it on message_start and
// as a non-streamed reply returns it.
func verifyShapeMessage(t *testing.T, where string, m map[string]any) {
	t.Helper()
	verifyShapeString(t, where, m, "id", true)
	verifyShapeString(t, where, m, "type", true)
	if m["type"] != "message" {
		t.Errorf("%s.type: %v, want \"message\"", where, m["type"])
	}
	verifyShapeString(t, where, m, "role", true)
	if m["role"] != "assistant" {
		t.Errorf("%s.role: %v, want \"assistant\"", where, m["role"])
	}
	verifyShapeString(t, where, m, "model", true)
	content, ok := m["content"].([]any)
	if !ok {
		t.Errorf("%s.content: %T (%v), want an array", where, m["content"], m["content"])
	} else {
		for i, v := range content {
			b := verifyShapeObject(t, fmt.Sprintf("%s.content[%d]", where, i), map[string]any{"content_block": v}, "content_block")
			if b != nil {
				verifyShapeBlock(t, fmt.Sprintf("%s.content[%d]", where, i), b)
			}
		}
	}
	if v, ok := m["stop_reason"]; !ok {
		t.Errorf("%s: stop_reason is missing", where)
	} else if v != nil {
		verifyShapeString(t, where, m, "stop_reason", true)
	}
	if v, ok := m["stop_sequence"]; !ok {
		t.Errorf("%s: stop_sequence is missing", where)
	} else if v != nil {
		verifyShapeString(t, where, m, "stop_sequence", false)
	}
	verifyShapeUsage(t, where, m, true)
}

// verifyShapeBlock checks a content_block object: every block has a type, and the field Claude
// Code reads on that type is present and of that type. A tool_use input is always an object.
func verifyShapeBlock(t *testing.T, where string, b map[string]any) {
	t.Helper()
	verifyShapeString(t, where, b, "type", true)
	switch b["type"] {
	case "text":
		verifyShapeString(t, where, b, "text", false)
	case "thinking":
		verifyShapeString(t, where, b, "thinking", false)
	case "tool_use", "server_tool_use":
		verifyShapeString(t, where, b, "id", true)
		verifyShapeString(t, where, b, "name", true)
		if _, ok := b["input"].(map[string]any); !ok {
			t.Errorf("%s.input: %T (%v), want a JSON object (Claude Code reads input.command and the like)", where, b["input"], b["input"])
		}
	}
}

// verifyShapeDelta checks a content_block_delta's delta, whose type names the field it carries.
func verifyShapeDelta(t *testing.T, where string, d map[string]any) {
	t.Helper()
	verifyShapeString(t, where, d, "type", true)
	switch d["type"] {
	case "text_delta":
		verifyShapeString(t, where, d, "text", false)
	case "thinking_delta":
		verifyShapeString(t, where, d, "thinking", false)
	case "input_json_delta":
		verifyShapeString(t, where, d, "partial_json", false)
	case "signature_delta":
		verifyShapeString(t, where, d, "signature", false)
	}
}

// verifyShapeStream checks a whole Messages stream: message_start first, content blocks that open
// and close in order, a message_delta with its stop_reason and usage, then message_stop, or an
// error event with Claude Code's error object. Every event and field is present and typed as the
// streaming page shows, because a null or missing one is a JavaScript TypeError in Claude Code.
func verifyShapeStream(t *testing.T, raw string) []verifyShapeEvent {
	t.Helper()
	events := verifyShapeEvents(t, raw)
	if len(events) == 0 {
		t.Fatalf("no SSE events in reply: %q", raw)
	}
	if events[0].name != "message_start" {
		t.Errorf("first event is %q, want message_start", events[0].name)
	}
	open := map[int]bool{}
	sawStop, sawMessageDelta, sawStopReason := false, false, false
	for i, ev := range events {
		where := fmt.Sprintf("event %d (%s)", i, ev.name)
		switch ev.name {
		case "message_start":
			if i != 0 {
				t.Errorf("%s: message_start is not first", where)
			}
			msg := verifyShapeObject(t, where, ev.data, "message")
			if msg != nil {
				verifyShapeMessage(t, where+".message", msg)
			}
		case "content_block_start":
			verifyShapeNumber(t, where+".index", ev.data["index"], true)
			idx, _ := ev.data["index"].(float64)
			if open[int(idx)] {
				t.Errorf("%s: content block %d is already open", where, int(idx))
			}
			open[int(idx)] = true
			block := verifyShapeObject(t, where, ev.data, "content_block")
			if block != nil {
				verifyShapeBlock(t, where+".content_block", block)
			}
		case "content_block_delta":
			verifyShapeNumber(t, where+".index", ev.data["index"], true)
			idx, _ := ev.data["index"].(float64)
			if !open[int(idx)] {
				t.Errorf("%s: delta for content block %d, which is not open", where, int(idx))
			}
			delta := verifyShapeObject(t, where, ev.data, "delta")
			if delta != nil {
				verifyShapeDelta(t, where+".delta", delta)
			}
		case "content_block_stop":
			verifyShapeNumber(t, where+".index", ev.data["index"], true)
			idx, _ := ev.data["index"].(float64)
			if !open[int(idx)] {
				t.Errorf("%s: stop for content block %d, which is not open", where, int(idx))
			}
			delete(open, int(idx))
		case "message_delta":
			if len(open) != 0 {
				t.Errorf("%s: message_delta while content blocks %v are still open", where, open)
			}
			delta := verifyShapeObject(t, where, ev.data, "delta")
			if delta != nil {
				verifyShapeString(t, where+".delta", delta, "stop_reason", true)
				sawStopReason = true
				if v, ok := delta["stop_sequence"]; !ok {
					t.Errorf("%s.delta: stop_sequence is missing", where)
				} else if v != nil {
					verifyShapeString(t, where+".delta", delta, "stop_sequence", false)
				}
			}
			verifyShapeUsage(t, where, ev.data, false)
			sawMessageDelta = true
		case "message_stop":
			if i != len(events)-1 {
				t.Errorf("%s: message_stop is not the last event", where)
			}
			if len(open) != 0 {
				t.Errorf("%s: content blocks %v never closed", where, open)
			}
			sawStop = true
		case "error":
			if i != len(events)-1 {
				t.Errorf("%s: error event is not the last event", where)
			}
			e := verifyShapeObject(t, where, ev.data, "error")
			if e != nil {
				verifyShapeString(t, where+".error", e, "type", true)
				verifyShapeString(t, where+".error", e, "message", true)
			}
			sawStop = true // an error ends the stream
		default:
			t.Errorf("%s: unexpected event type", where)
		}
	}
	if !sawStop {
		t.Errorf("stream ended without message_stop or error")
	}
	if sawStopReason && !sawMessageDelta {
		t.Errorf("stop_reason without message_delta")
	}
	return events
}

// verifyShapeCore starts core with one account for the fake upstream, routed by wire API.
func verifyShapeCore(t *testing.T, u *upstream, wire string) string {
	t.Helper()
	return startCore(t, u, func(cfg map[string]any) {
		acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
		acct["models"] = []string{"gpt-test"}
		settings := acct["settings"].(map[string]any)
		if wire != "" {
			settings["wire_api"] = wire
		}
		if wire == "gemini" {
			settings["base_url"] = u.URL + "/v1beta"
		}
	})
}

// verifyShapeReply posts one Messages request and returns the status and body.
func verifyShapeReply(t *testing.T, base, body string) (int, string) {
	t.Helper()
	resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// TestVerifyMessagesReplyShapeFromOddUpstreams checks what a Messages client, and so Claude Code,
// reads in a reply rendered from a non-Claude upstream (OpenAI Chat Completions, Codex Responses,
// Gemini): an empty reply, one made only of reasoning, a tool call with no arguments, and an
// error mid-stream, streamed and not. It follows CLIProxyAPI issues #3776 (a Bash call arriving
// with undefined input.command), #137 (a null value where a typed object was read), #317 and
// #2134 (an undefined field in a message).
func TestVerifyMessagesReplyShapeFromOddUpstreams(t *testing.T) {
	const streamBody = `{"model":"gpt-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	const plainBody = `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`

	t.Run("chat empty reply, streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			write := func(s string) {
				fmt.Fprintf(w, "data: %s\n\n", s)
				f.Flush()
			}
			const head = `"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test-upstream"`
			write(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":null},"finish_reason":"stop"}]}`)
			write(`{` + head + `,"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
			write(`[DONE]`)
		}
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "chat"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		verifyShapeStream(t, raw)
	})

	t.Run("chat empty reply, not streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-test-upstream",
				"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
		}
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "chat"), plainBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("reply is not a JSON object: %v: %s", err, raw)
		}
		verifyShapeMessage(t, "reply", msg)
	})

	t.Run("chat tool call with empty arguments, streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			write := func(s string) {
				fmt.Fprintf(w, "data: %s\n\n", s)
				f.Flush()
			}
			const head = `"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test-upstream"`
			write(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":null}]}`)
			write(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			write(`{` + head + `,"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
			write(`[DONE]`)
		}
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "chat"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		events := verifyShapeStream(t, raw)
		if !verifyShapeHasToolUse(t, events) {
			t.Errorf("streamed reply has no tool_use block: %s", raw)
		}
	})

	t.Run("chat tool call with empty arguments, not streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-test-upstream",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
		}
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "chat"), plainBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		verifyShapeNonStreamToolUse(t, raw)
	})

	t.Run("responses reasoning only, streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			write := func(s string) {
				fmt.Fprintf(w, "data: %s\n\n", s)
				f.Flush()
			}
			write(`{"type":"response.created","response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-test-upstream","output":[]}}`)
			write(`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`)
			write(`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Thinking."}`)
			write(`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Thinking."}]}}`)
			write(`{"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-test-upstream","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Thinking."}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`)
		})
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "responses"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		verifyShapeStream(t, raw)
	})

	t.Run("responses tool call with empty arguments, not streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-test-upstream",
				"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Bash","arguments":""}],
				"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
		})
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "responses"), plainBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		verifyShapeNonStreamToolUse(t, raw)
	})

	t.Run("responses error mid-stream", func(t *testing.T) {
		u := newUpstream(t)
		u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			write := func(s string) {
				fmt.Fprintf(w, "data: %s\n\n", s)
				f.Flush()
			}
			write(`{"type":"response.created","response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-test-upstream","output":[]}}`)
			write(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hi"}`)
			write(`{"type":"error","code":"server_error","message":"upstream broke"}`)
		})
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "responses"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		events := verifyShapeStream(t, raw)
		if len(events) == 0 || events[len(events)-1].name != "error" {
			t.Errorf("stream does not end in an error event: %s", raw)
		}
	})

	t.Run("gemini empty reply, streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"modelVersion":"gpt-test-upstream","usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":0,"totalTokenCount":3}}`+"\n\n")
		})
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "gemini"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		verifyShapeStream(t, raw)
	})

	t.Run("gemini tool call without arguments, streamed", func(t *testing.T) {
		u := newUpstream(t)
		u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"Bash"}}]},"finishReason":"STOP","index":0}],"modelVersion":"gpt-test-upstream","usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`+"\n\n")
		})
		status, raw := verifyShapeReply(t, verifyShapeCore(t, u, "gemini"), streamBody)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		events := verifyShapeStream(t, raw)
		if !verifyShapeHasToolUse(t, events) {
			t.Errorf("streamed reply has no tool_use block: %s", raw)
		}
	})
}

// verifyShapeHasToolUse reports whether the stream opened a tool_use block.
func verifyShapeHasToolUse(t *testing.T, events []verifyShapeEvent) bool {
	t.Helper()
	for _, ev := range events {
		if ev.name != "content_block_start" {
			continue
		}
		if b, ok := ev.data["content_block"].(map[string]any); ok && b["type"] == "tool_use" {
			return true
		}
	}
	return false
}

// verifyShapeNonStreamToolUse checks a non-streamed reply's tool_use block has the object input
// Claude Code reads input.command and the like from.
func verifyShapeNonStreamToolUse(t *testing.T, raw string) {
	t.Helper()
	var msg map[string]any
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("reply is not a JSON object: %v: %s", err, raw)
	}
	verifyShapeMessage(t, "reply", msg)
	content, _ := msg["content"].([]any)
	for i, v := range content {
		b, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			if _, ok := b["input"].(map[string]any); !ok {
				t.Errorf("reply.content[%d].input: %T (%v), want a JSON object", i, b["input"], b["input"])
			}
			return
		}
	}
	t.Errorf("reply has no tool_use block: %s", raw)
}
