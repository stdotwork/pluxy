package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A Codex client's custom, namespace, additional_tools and client-run tool_search tools, through
// a real core and the real extensions, against scripted Messages, GenerateContent and Chat
// upstreams: what goes upstream and what the client gets back.

// toolUpstream answers each request with one scripted function call, or with text when it has
// none, in its wire format, streamed or not as it was asked.
type toolUpstream struct {
	*httptest.Server
	wire string // chat, messages or gemini

	mu     sync.Mutex
	bodies []map[string]any

	name   string // the function the model calls
	args   string // its arguments, as JSON text
	cutOff bool   // the reply ends on the token limit
}

func newToolUpstream(t *testing.T, wire, name, args string, cutOff bool) *toolUpstream {
	u := &toolUpstream{wire: wire, name: name, args: args, cutOff: cutOff}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("upstream got %s: %v", raw, err)
		}
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()
		streamed, _ := body["stream"].(bool)
		if wire == "gemini" {
			streamed = strings.Contains(r.URL.Path, "streamGenerateContent")
		}
		switch wire {
		case "chat":
			u.replyChat(w, streamed)
		case "messages":
			u.replyMessages(w, streamed)
		default:
			u.replyGemini(w, streamed)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *toolUpstream) sent(t *testing.T) map[string]any {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(u.bodies))
	}
	return u.bodies[0]
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
}

// pieces cuts arguments in two, as a stream delivers them.
func pieces(args string) []string {
	if len(args) < 2 {
		return []string{args}
	}
	return []string{args[:len(args)/2], args[len(args)/2:]}
}

func (u *toolUpstream) replyChat(w http.ResponseWriter, streamed bool) {
	finish := "tool_calls"
	if u.cutOff {
		finish = "length"
	}
	if !streamed {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-up", "object": "chat.completion", "created": 1700000000, "model": "up",
			"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": map[string]any{"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{"id": "call_up", "type": "function", "function": map[string]any{"name": u.name, "arguments": u.args}}}}}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta map[string]any, reason any) {
		writeSSE(w, "", map[string]any{"id": "chatcmpl-up", "object": "chat.completion.chunk", "created": 1700000000, "model": "up",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
	}
	chunk(map[string]any{"role": "assistant", "content": nil}, nil)
	chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_up", "type": "function",
		"function": map[string]any{"name": u.name, "arguments": ""}}}}, nil)
	for _, p := range pieces(u.args) {
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": p}}}}, nil)
	}
	chunk(map[string]any{}, finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (u *toolUpstream) replyMessages(w http.ResponseWriter, streamed bool) {
	stop := "tool_use"
	if u.cutOff {
		stop = "max_tokens"
	}
	if !streamed {
		// A non-streamed Messages reply holds the input as an object, so arguments that are not
		// one arrive as the object an upstream would have parsed them into.
		var input any = map[string]any{"partial": "x"}
		_ = json.Unmarshal([]byte(u.args), &input)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "msg_up", "type": "message", "role": "assistant", "model": "up", "stop_reason": stop, "stop_sequence": nil,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_up", "name": u.name, "input": input}},
			"usage":   map[string]any{"input_tokens": 3, "output_tokens": 4}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSE(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_up", "type": "message", "role": "assistant",
		"content": []any{}, "model": "up", "stop_reason": nil, "usage": map[string]any{"input_tokens": 3, "output_tokens": 1}}})
	writeSSE(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": "toolu_up", "name": u.name, "input": map[string]any{}}})
	for _, p := range pieces(u.args) {
		writeSSE(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": p}})
	}
	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSE(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 4}})
	writeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
}

func (u *toolUpstream) replyGemini(w http.ResponseWriter, streamed bool) {
	finish := "STOP"
	if u.cutOff {
		finish = "MAX_TOKENS"
	}
	// GenerateContent holds a call's arguments as an object, so arguments that are not one arrive
	// as another shape.
	var args any = map[string]any{"partial": "x"}
	_ = json.Unmarshal([]byte(u.args), &args)
	reply := map[string]any{"responseId": "resp-up", "modelVersion": "up",
		"candidates":    []any{map[string]any{"index": 0, "finishReason": finish, "content": map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": u.name, "args": args}}}}}},
		"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 4, "totalTokenCount": 7}}
	if !streamed {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSE(w, "", reply)
}

// toolTargets are the real extensions, each with the upstream format it speaks.
var toolTargets = []struct {
	name, wire, manifest string
	settings             func(url string) map[string]any
}{
	{"claude", "messages", "claude", func(url string) map[string]any { return map[string]any{"base_url": url} }},
	{"gemini", "gemini", "gemini", func(url string) map[string]any { return map[string]any{"base_url": url} }},
	{"openai-compatible-chat", "chat", "", func(url string) map[string]any { return map[string]any{"base_url": url + "/v1"} }},
	{"openai-compatible-messages", "messages", "", func(url string) map[string]any {
		return map[string]any{"base_url": url + "/v1", "wire_api": "messages"}
	}},
}

const toolModel = "tools-model"

func startToolCore(t *testing.T, manifest string, settings map[string]any) string {
	t.Helper()
	path := filepath.Join(binDir, "pluxy-extension.json")
	if manifest != "" {
		path = filepath.Join(binDir, manifest, "pluxy-extension.json")
	}
	return runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": path,
			"accounts": []any{map[string]any{"id": "tools-1", "settings": settings, "models": []string{toolModel},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
		},
	})
}

// sentToolNames reads the function names a request declared to its upstream.
func sentToolNames(wire string, body map[string]any) []string {
	var names []string
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		switch wire {
		case "chat":
			if f, ok := tool["function"].(map[string]any); ok {
				names = append(names, f["name"].(string))
			}
		case "messages":
			if n, ok := tool["name"].(string); ok {
				names = append(names, n)
			}
		default:
			decls, _ := tool["functionDeclarations"].([]any)
			for _, d := range decls {
				names = append(names, d.(map[string]any)["name"].(string))
			}
		}
	}
	return names
}

// sentChoice reads the function names a request's tool choice names.
func sentChoice(wire string, body map[string]any) []string {
	switch wire {
	case "chat":
		c, _ := body["tool_choice"].(map[string]any)
		if f, ok := c["function"].(map[string]any); ok {
			return []string{f["name"].(string)}
		}
	case "messages":
		c, _ := body["tool_choice"].(map[string]any)
		if n, ok := c["name"].(string); ok {
			return []string{n}
		}
	default:
		cfg, _ := body["toolConfig"].(map[string]any)
		fc, _ := cfg["functionCallingConfig"].(map[string]any)
		list, _ := fc["allowedFunctionNames"].([]any)
		var names []string
		for _, n := range list {
			names = append(names, n.(string))
		}
		return names
	}
	return nil
}

type sentCall struct {
	name  string
	input map[string]any
}

// sentHistory reads a request's replayed calls and the results they got, by call ID.
func sentHistory(t *testing.T, wire string, body map[string]any) ([]sentCall, map[string]string) {
	t.Helper()
	var calls []sentCall
	results := map[string]string{}
	switch wire {
	case "chat":
		msgs, _ := body["messages"].([]any)
		for _, raw := range msgs {
			m := raw.(map[string]any)
			tcs, _ := m["tool_calls"].([]any)
			for _, tc := range tcs {
				f := tc.(map[string]any)["function"].(map[string]any)
				var input map[string]any
				if err := json.Unmarshal([]byte(f["arguments"].(string)), &input); err != nil {
					t.Fatalf("arguments %q: %v", f["arguments"], err)
				}
				calls = append(calls, sentCall{f["name"].(string), input})
			}
			if m["role"] == "tool" {
				results[m["tool_call_id"].(string)], _ = m["content"].(string)
			}
		}
	case "messages":
		msgs, _ := body["messages"].([]any)
		for _, raw := range msgs {
			blocks, _ := raw.(map[string]any)["content"].([]any)
			for _, b := range blocks {
				block := b.(map[string]any)
				switch block["type"] {
				case "tool_use":
					calls = append(calls, sentCall{block["name"].(string), block["input"].(map[string]any)})
				case "tool_result":
					results[block["tool_use_id"].(string)], _ = block["content"].(string)
				}
			}
		}
	default:
		contents, _ := body["contents"].([]any)
		for _, raw := range contents {
			parts, _ := raw.(map[string]any)["parts"].([]any)
			for _, p := range parts {
				part := p.(map[string]any)
				if c, ok := part["functionCall"].(map[string]any); ok {
					calls = append(calls, sentCall{c["name"].(string), c["args"].(map[string]any)})
				}
				if r, ok := part["functionResponse"].(map[string]any); ok {
					results[r["id"].(string)], _ = r["response"].(map[string]any)["result"].(string)
				}
			}
		}
	}
	return calls, results
}

type toolEvent struct {
	typ  string
	data map[string]any
}

// postTools sends a Responses request and reads a streamed reply's events.
func postToolStream(t *testing.T, base, body string) []toolEvent {
	t.Helper()
	resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out []toolEvent
	for _, block := range strings.Split(string(raw), "\n\n") {
		_, data, ok := strings.Cut(block, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("event %q: %v", data, err)
		}
		typ, _ := m["type"].(string)
		out = append(out, toolEvent{typ, m})
	}
	return out
}

func firstEvent(events []toolEvent, typ string) map[string]any {
	for _, e := range events {
		if e.typ == typ {
			return e.data
		}
	}
	return nil
}

func wantItem(t *testing.T, where string, item any, want map[string]any) {
	t.Helper()
	got, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("%s: item = %v", where, item)
	}
	for k, v := range want {
		if v == nil {
			if _, has := got[k]; has {
				t.Errorf("%s: %s = %v, want none", where, k, got[k])
			}
			continue
		}
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			t.Errorf("%s: %s = %v, want %v", where, k, got[k], v)
		}
	}
}

func oneOutput(t *testing.T, response map[string]any) any {
	t.Helper()
	out, _ := response["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output = %v, want one item", response["output"])
	}
	return out[0]
}

const (
	toolPatch   = `{"type":"custom","name":"apply_patch","description":"Edit files.","format":{"type":"grammar","syntax":"lark","definition":"start: begin_patch"}}`
	toolWeather = `{"type":"function","name":"get_weather","description":"Weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}`
	toolSearch  = `{"type":"tool_search","execution":"client","description":"Find tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}}`
)

func toolsBody(streamed bool) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"tool_choice":{"type":"function","name":"get_weather","namespace":"functions"},
		"tools":[%s,{"type":"namespace","name":"functions","description":"Plain tools.","tools":[%s]},
			{"type":"namespace","name":"mcp__linear__","description":"Linear issues.","tools":[{"type":"function","name":"create_issue","parameters":{"type":"object","properties":{"title":{"type":"string"}}}}]},
			%s,{"type":"function","name":"note","description":"Declared first, so it stays.","parameters":{"type":"object"}}],
		"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"note","description":"Same name, declared later."},{"type":"custom","name":"extra","description":"From the item."}]},
			{"type":"message","role":"user","content":"What is the weather in Oslo?"},
			{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},
			{"type":"custom_tool_call_output","call_id":"call_patch","output":"Done"},
			{"type":"function_call","call_id":"call_issue","name":"create_issue","namespace":"mcp__linear__","arguments":"{\"title\":\"x\"}"},
			{"type":"function_call_output","call_id":"call_issue","output":"ok"}]}`,
		toolModel, streamed, toolPatch, toolWeather, toolSearch)
}

func customBody(streamed bool) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"input":"Edit the file","tools":[%s]}`, toolModel, streamed, toolPatch)
}

// What each upstream is sent for a Codex client's tools, and the reply restored to its namespace.
func TestCodexToolsAtTheHTTPSeam(t *testing.T) {
	for _, tg := range toolTargets {
		for _, streamed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tg.name, streamed), func(t *testing.T) {
				u := newToolUpstream(t, tg.wire, "get_weather", `{"city":"Oslo"}`, false)
				base := startToolCore(t, tg.manifest, tg.settings(u.URL))
				want := map[string]any{"type": "function_call", "name": "get_weather", "namespace": "functions", "arguments": `{"city":"Oslo"}`, "status": "completed"}
				if streamed {
					got := postToolStream(t, base, toolsBody(true))
					done := firstEvent(got, "response.output_item.done")
					if done == nil {
						t.Fatalf("no done item in %v", got)
					}
					wantItem(t, "done", done["item"], want)
					wantItem(t, "final", oneOutput(t, firstEvent(got, "response.completed")["response"].(map[string]any)), want)
				} else {
					resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, toolsBody(false))
					defer resp.Body.Close()
					raw, _ := io.ReadAll(resp.Body)
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("status %d: %s", resp.StatusCode, raw)
					}
					var r map[string]any
					if err := json.Unmarshal(raw, &r); err != nil {
						t.Fatal(err)
					}
					wantItem(t, "output", oneOutput(t, r), want)
				}

				body := u.sent(t)
				if got, want := sentToolNames(tg.wire, body), []string{"apply_patch", "get_weather", "mcp__linear__create_issue", "tool_search", "note", "extra"}; !slices.Equal(got, want) {
					t.Errorf("tools = %v, want %v", got, want)
				}
				if got := sentChoice(tg.wire, body); !slices.Equal(got, []string{"get_weather"}) {
					t.Errorf("tool choice = %v", got)
				}
				calls, results := sentHistory(t, tg.wire, body)
				if len(calls) != 2 || calls[0].name != "apply_patch" || calls[0].input["input"] != "*** Begin Patch\n*** End Patch" ||
					calls[1].name != "mcp__linear__create_issue" || calls[1].input["title"] != "x" {
					t.Errorf("replayed calls = %+v", calls)
				}
				if results["call_patch"] != "Done" || results["call_issue"] != "ok" {
					t.Errorf("replayed results = %v", results)
				}
			})
		}
	}
}

// A whole custom call comes back with the events a Codex client expects.
func TestCodexCustomToolCallAtTheHTTPSeam(t *testing.T) {
	const input = "*** Begin Patch\n*** Add File: a.txt\n+é\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"input": input})
	for _, tg := range toolTargets {
		for _, streamed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tg.name, streamed), func(t *testing.T) {
				u := newToolUpstream(t, tg.wire, "apply_patch", string(args), false)
				base := startToolCore(t, tg.manifest, tg.settings(u.URL))
				want := map[string]any{"type": "custom_tool_call", "name": "apply_patch", "input": input, "arguments": nil, "status": "completed"}
				if !streamed {
					resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, customBody(false))
					defer resp.Body.Close()
					raw, _ := io.ReadAll(resp.Body)
					var r map[string]any
					if err := json.Unmarshal(raw, &r); err != nil || resp.StatusCode != http.StatusOK {
						t.Fatalf("status %d: %s", resp.StatusCode, raw)
					}
					wantItem(t, "output", oneOutput(t, r), want)
					return
				}
				got := postToolStream(t, base, customBody(true))
				added := firstEvent(got, "response.output_item.added")
				if added == nil {
					t.Fatalf("no added item in %v", got)
				}
				wantItem(t, "added", added["item"], map[string]any{"type": "custom_tool_call", "name": "apply_patch", "input": "", "status": "in_progress"})
				var text strings.Builder
				for _, e := range got {
					if e.typ == "response.custom_tool_call_input.delta" {
						text.WriteString(e.data["delta"].(string))
					}
				}
				if text.String() != input {
					t.Errorf("deltas = %q, want %q", text.String(), input)
				}
				if d := firstEvent(got, "response.custom_tool_call_input.done"); d == nil || d["input"] != input {
					t.Errorf("input done = %v", d)
				}
				wantItem(t, "done", firstEvent(got, "response.output_item.done")["item"], want)
				wantItem(t, "final", oneOutput(t, firstEvent(got, "response.completed")["response"].(map[string]any)), want)
			})
		}
	}
}

// Arguments the token limit cut off never reach the client as a call: Codex runs a done item
// whatever its status says. A non-streamed Messages or Gemini reply holds its arguments as an
// object, so its not-whole arguments are another shape.
func TestCodexCustomToolCallCutOffAtTheHTTPSeam(t *testing.T) {
	const cut = `{"input":"rm -rf /home/me/project/build`
	for _, tg := range toolTargets {
		for _, streamed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tg.name, streamed), func(t *testing.T) {
				u := newToolUpstream(t, tg.wire, "apply_patch", cut, true)
				base := startToolCore(t, tg.manifest, tg.settings(u.URL))
				if streamed {
					got := postToolStream(t, base, customBody(true))
					if firstEvent(got, "response.custom_tool_call_input.done") != nil || firstEvent(got, "response.output_item.done") != nil {
						t.Errorf("the client got the call: %v", got)
					}
					final := firstEvent(got, "response.incomplete")
					if final == nil {
						t.Fatalf("no response.incomplete in %v", got)
					}
					if out, _ := final["response"].(map[string]any)["output"].([]any); len(out) != 0 {
						t.Errorf("final output = %v", out)
					}
					return
				}
				resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, customBody(false))
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				var r map[string]any
				if err := json.Unmarshal(raw, &r); err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("status %d: %s", resp.StatusCode, raw)
				}
				if out, _ := r["output"].([]any); r["status"] != "incomplete" || len(out) != 0 {
					t.Errorf("status %v, output %v", r["status"], out)
				}
			})
		}
	}
}

// On an ordinary finish, arguments that are not one input string fail the reply through the path
// any upstream error takes, naming the tool and not repeating them.
func TestCodexCustomToolCallNotWholeFailsAtTheHTTPSeam(t *testing.T) {
	const args = `{"patch":"secret-looking-text"}`
	for _, tg := range toolTargets {
		for _, streamed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tg.name, streamed), func(t *testing.T) {
				u := newToolUpstream(t, tg.wire, "apply_patch", args, false)
				base := startToolCore(t, tg.manifest, tg.settings(u.URL))
				if streamed {
					got := postToolStream(t, base, customBody(true))
					if firstEvent(got, "response.output_item.done") != nil || firstEvent(got, "response.completed") != nil {
						t.Errorf("the client got a call or a success: %v", got)
					}
					e := firstEvent(got, "error")
					if e == nil || !strings.Contains(fmt.Sprint(e["message"]), "apply_patch") || strings.Contains(fmt.Sprint(e), "secret-looking-text") {
						t.Errorf("error event = %v", e)
					}
					return
				}
				resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, customBody(false))
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode < 500 || !strings.Contains(string(raw), "apply_patch") || strings.Contains(string(raw), "secret-looking-text") {
					t.Errorf("status %d: %s", resp.StatusCode, raw)
				}
			})
		}
	}
}

// A call that is not whole fails that one reply and leaves the account usable: the next request
// gets the same answer, not a cooldown.
func TestCodexCustomToolCallNotWholeKeepsAccountUsable(t *testing.T) {
	const args = `{"patch":"x"}`
	for _, tg := range toolTargets {
		t.Run(tg.name, func(t *testing.T) {
			u := newToolUpstream(t, tg.wire, "apply_patch", args, false)
			base := startToolCore(t, tg.manifest, tg.settings(u.URL))
			for i := range 2 {
				resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, customBody(false))
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "apply_patch") {
					t.Fatalf("request %d: status %d: %s", i+1, resp.StatusCode, raw)
				}
			}
		})
	}
}

// A name past the upstream's limit is repaired there, and the client gets its own name back.
func TestCodexToolNameRepairAtTheHTTPSeam(t *testing.T) {
	long := strings.Repeat("a", 150)
	ns := "mcp__" + strings.Repeat("s", 40) + "__"
	body := fmt.Sprintf(`{"model":%q,"input":"go","tools":[{"type":"namespace","name":%q,"tools":[{"type":"function","name":%q,"parameters":{"type":"object"}}]}]}`, toolModel, ns, long)
	for _, tg := range toolTargets {
		t.Run(tg.name, func(t *testing.T) {
			// First see the name the upstream is sent, then have the model call it.
			probe := newToolUpstream(t, tg.wire, "unused", `{}`, false)
			base := startToolCore(t, tg.manifest, tg.settings(probe.URL))
			resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			names := sentToolNames(tg.wire, probe.sent(t))
			if len(names) != 1 || names[0] == ns+long {
				t.Fatalf("sent names = %v", names)
			}

			u := newToolUpstream(t, tg.wire, names[0], `{}`, false)
			base = startToolCore(t, tg.manifest, tg.settings(u.URL))
			resp = postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var r map[string]any
			if err := json.Unmarshal(raw, &r); err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			wantItem(t, "output", oneOutput(t, r), map[string]any{"type": "function_call", "name": long, "namespace": ns})
		})
	}
}

// A list of one namespaced function reaches each upstream as its flattened name.
func TestCodexAllowedToolsNamespaceAtTheHTTPSeam(t *testing.T) {
	body := fmt.Sprintf(`{"model":%q,"input":"go","tools":[{"type":"namespace","name":"docs","tools":[{"type":"function","name":"search","parameters":{"type":"object"}},{"type":"function","name":"fetch","parameters":{"type":"object"}}]}],
		"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"search","namespace":"docs"}]}}`, toolModel)
	for _, tg := range toolTargets {
		t.Run(tg.name, func(t *testing.T) {
			u := newToolUpstream(t, tg.wire, "docs__search", `{}`, false)
			base := startToolCore(t, tg.manifest, tg.settings(u.URL))
			resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			var r map[string]any
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			wantItem(t, "output", oneOutput(t, r), map[string]any{"type": "function_call", "name": "search", "namespace": "docs"})
			sent := u.sent(t)
			switch tg.wire {
			case "messages": // Messages has no allowed list: only the allowed tool is defined.
				if got := sentToolNames(tg.wire, sent); !slices.Equal(got, []string{"docs__search"}) {
					t.Errorf("tools = %v", got)
				}
			case "gemini":
				if got := sentChoice(tg.wire, sent); !slices.Equal(got, []string{"docs__search"}) {
					t.Errorf("allowed names = %v", got)
				}
			default:
				if !strings.Contains(fmt.Sprint(sent["tool_choice"]), "docs__search") {
					t.Errorf("tool choice = %v", sent["tool_choice"])
				}
			}
		})
	}
}
