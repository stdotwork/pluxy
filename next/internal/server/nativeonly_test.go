package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format"
	"github.com/stdotwork/pluxy/next/format/formattest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/protocol/nativeonly"
)

// nativeOnlyRequest is one client request the differential test sends through both frames.
type nativeOnlyRequest struct {
	src     string // corpus, format tests or edge cases
	name    string
	path    string
	headers map[string]string
	body    []byte
}

// TestNativeOnlyFrameGivesTheSameRequest is the differential test of native-only generate calls
// (protocol minor 27): for every generate request the corpus cases send and every request body
// the format packages' tests hold as a literal, a handler that gets it in a native-only call (core
// leaves its items, tools and origin out and the extension parses its native body) gets the same
// llm.Request, field for field, as one that gets the full call. Each request goes as core reads
// it, with its thinking suffix taken, and also renamed to another upstream model.
func TestNativeOnlyFrameGivesTheSameRequest(t *testing.T) {
	reqs := append(corpusGenerateRequests(t), formatTestRequests(t)...)
	// Odd text, once in valid UTF-8 (which goes native-only) and once with invalid bytes (which
	// does not).
	reqs = append(reqs, nativeOnlyEdgeRequests("a<b>&c\u2028d\u2029e\\ud800f\\u0000g\U0001F600 \\\"q\\\"")...)
	reqs = append(reqs, nativeOnlyEdgeRequests("a<b>&c\xff\xfeg")...)

	parses := protocol.Info{ProtocolMinor: protocol.Minor, NativeParser: format.ParserVersion}
	ran, nativeOnly := 0, 0
	read := map[string]int{}
	for _, c := range reqs {
		req, ok := readAsCore(t, c)
		if !ok {
			continue
		}
		read[c.src]++
		for _, v := range []struct {
			name string
			req  llm.Request
		}{
			{"as read", req},
			{"renamed", route.Renamed(req, "upstream-model-x")},
		} {
			ran++
			want := throughFrame(t, protocol.Call{Type: protocol.TypeCall, Op: protocol.OpGenerate, Request: &v.req})
			if !host.ParsesNative(parses) || !nativeonly.Can(v.req) {
				continue
			}
			nativeOnly++
			sent := nativeonly.Strip(v.req)
			call := throughFrame(t, protocol.Call{Type: protocol.TypeCall, Op: protocol.OpGenerate, Request: &sent, NativeOnly: true})
			if !call.NativeOnly || len(call.Request.Items) != 0 || call.Request.Tools != nil || call.Request.Origin != nil {
				t.Fatalf("%s (%s): the native-only frame carries the parsed request", c.name, v.name)
			}
			got := call.Request
			if err := nativeonly.Parse(got); err != nil {
				t.Errorf("%s (%s): %v", c.name, v.name, err)
				continue
			}
			if !reflect.DeepEqual(got, want.Request) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want.Request)
				t.Errorf("%s (%s): native-only request differs\n got: %.2000s\nwant: %.2000s", c.name, v.name, g, w)
			}
		}
	}
	t.Logf("%d generate requests core reads (%d from corpus cases, %d from format tests, %d edge cases), %d variants, %d of them native-only",
		ran/2, read["corpus"], read["format tests"], read["edge cases"], ran, nativeOnly)
	if nativeOnly < 100 {
		t.Fatalf("only %d variants went native-only", nativeOnly)
	}
}

// TestNativeOnlyLeavesChangedBodiesWhole checks that core sends the full request when the native
// body is no longer the one its items were parsed from: a thinking suffix rewrites the body, and
// a Gemini body's own "model" is part of the request's origin, so renaming it changes that.
func TestNativeOnlyLeavesChangedBodiesWhole(t *testing.T) {
	for _, c := range []nativeOnlyRequest{
		{name: "chat suffix", path: "/v1/chat/completions", body: []byte(`{"model":"m(high)","messages":[{"role":"user","content":"hi"}]}`)},
		{name: "responses suffix", path: "/v1/responses", body: []byte(`{"model":"m(16000)","input":"hi"}`)},
		{name: "messages suffix", path: "/v1/messages", body: []byte(`{"model":"m(low)","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)},
	} {
		req, ok := readAsCore(t, c)
		if !ok {
			t.Fatalf("%s: does not parse", c.name)
		}
		if req.Options.ThinkingSuffix == "" || nativeonly.Can(req) {
			t.Errorf("%s: suffix %q, native-only %v; want a suffix and a full call", c.name, req.Options.ThinkingSuffix, nativeonly.Can(req))
		}
	}
	gemini, ok := readAsCore(t, nativeOnlyRequest{name: "gemini", path: "/v1beta/models/g:generateContent",
		body: []byte(`{"model":"g","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)})
	if !ok || !nativeonly.Can(gemini) {
		t.Fatalf("gemini as read: parsed %v, native-only %v; want both", ok, ok && nativeonly.Can(gemini))
	}
	if nativeonly.Can(route.Renamed(gemini, "other")) {
		t.Errorf("a Gemini body whose own model was renamed goes native-only")
	}
	invalid, ok := readAsCore(t, nativeOnlyRequest{name: "invalid UTF-8", path: "/v1/messages",
		body: []byte("{\"model\":\"m\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"a\xffb\"}]}")})
	if !ok || nativeonly.Can(invalid) {
		t.Errorf("a body that is not valid UTF-8: parsed %v, goes native-only", ok)
	}
	chat, _ := readAsCore(t, nativeOnlyRequest{name: "chat", path: "/v1/chat/completions", body: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)})
	if !nativeonly.Can(route.Renamed(chat, "other")) {
		t.Errorf("a renamed Chat body does not go native-only")
	}
}

// readAsCore reads c as serve does up to routing: the format's parser, the parsed record, the
// drop policy, the thinking suffix and the native headers. It reports false for a request core
// refuses before routing.
func readAsCore(t *testing.T, c nativeOnlyRequest) (llm.Request, bool) {
	t.Helper()
	path, rest := c.path, ""
	var f clientFormat
	switch {
	case path == "/v1/chat/completions":
		f = chatFormat
	case path == "/v1/completions":
		f = completionsFormat
	case path == "/v1/responses" || path == "/backend-api/codex/responses":
		f = responsesFormat
	case path == "/v1/messages":
		f = messagesFormat
	case strings.HasPrefix(path, "/v1beta/models/"):
		rest = strings.TrimPrefix(path, "/v1beta/models/")
		if i := strings.IndexByte(rest, '?'); i >= 0 {
			rest = rest[:i]
		}
		if _, method, _ := strings.Cut(rest, ":"); method != "generateContent" && method != "streamGenerateContent" {
			return llm.Request{}, false
		}
		f = geminiFormat
	default:
		return llm.Request{}, false
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(c.body))
	for k, v := range c.headers {
		r.Header.Set(k, v)
	}
	if rest != "" {
		r.SetPathValue("action", rest)
	}
	req, err := f.parse(r, c.body)
	if err != nil {
		return llm.Request{}, false
	}
	markParsed(&req)
	req.DropPolicy = llm.DropReport
	takeThinkingSuffix(&req)
	f.nativeHeaders(r, &req)
	req.Caller = "caller-1"
	return req, true
}

// throughFrame writes call as core does and reads it as an extension does.
func throughFrame(t *testing.T, call protocol.Call) protocol.Call {
	t.Helper()
	var buf bytes.Buffer
	if err := protocol.WriteFrame(&buf, call); err != nil {
		t.Fatal(err)
	}
	var got protocol.Call
	if err := protocol.ReadFrame(&buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Request == nil {
		t.Fatal("the frame has no request")
	}
	return got
}

// corpusGenerateRequests returns the HTTP requests of every corpus case (client and then), as
// the case files hold them. readAsCore keeps those to generate routes.
func corpusGenerateRequests(t *testing.T) []nativeOnlyRequest {
	t.Helper()
	reqs, err := formattest.CorpusRequests(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return asNativeOnlyRequests(reqs)
}

// formatTestRequests returns every string literal in the format packages' tests that is a JSON
// object, as a request to each route that reads the package's format (readAsCore drops those that
// are not requests).
func formatTestRequests(t *testing.T) []nativeOnlyRequest {
	t.Helper()
	reqs, err := formattest.LiteralRequests(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return asNativeOnlyRequests(reqs)
}

func asNativeOnlyRequests(reqs []formattest.Request) []nativeOnlyRequest {
	out := make([]nativeOnlyRequest, len(reqs))
	for i, r := range reqs {
		out[i] = nativeOnlyRequest{src: r.Source, name: r.Name, path: r.Path, headers: r.Headers, body: r.Body}
	}
	return out
}

// nativeOnlyEdgeRequests are bodies whose raw parts a frame could change: whitespace inside
// kept JSON, repeated keys and unknown fields at every level, and odd, raw in a JSON string, in
// every kind of text.
func nativeOnlyEdgeRequests(odd string) []nativeOnlyRequest {
	return []nativeOnlyRequest{
		{src: "edge cases", name: "chat edge", path: "/v1/chat/completions", headers: map[string]string{"X-Unused": "1"}, body: []byte(`{
  "model": "m", "model": "m2", "x_top": {"b": 1,  "a": [ 1, 2 ]},
  "messages": [
    {"role": "system", "content": "` + odd + `", "name": "s"},
    {"role": "user", "content": [{"type": "text", "text": "` + odd + `", "x": { "y" : 1 }}, {"type": "image_url", "image_url": {"url": "https://e/x.png", "detail": "low"}}]},
    {"role": "assistant", "content": null, "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "f", "arguments": "{ \"a\" : 1 }"}}]},
    {"role": "tool", "tool_call_id": "c1", "content": "` + odd + `"}
  ],
  "tools": [{"type": "function", "function": {"name": "f", "description": "` + odd + `", "parameters": {
     "type": "object",   "properties": {"a": {"type": "integer", "description": "<&>"}}}, "strict": true}}],
  "tool_choice": {"type": "function", "function": {"name": "f"}},
  "response_format": {"type": "json_schema", "json_schema": {"name": "s", "schema": { "type" : "object" }}},
  "stream": true, "stream_options": {"include_usage": true}, "temperature": 0.5
}`)},
		{src: "edge cases", name: "responses edge", path: "/v1/responses", headers: map[string]string{"Session_id": "s1"}, body: []byte(`{
  "model": "m", "instructions": "` + odd + `", "store": false, "include": ["reasoning.encrypted_content"],
  "input": [
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "` + odd + `"}]},
    {"type": "reasoning", "id": "r1", "summary": [{"type": "summary_text", "text": "s"}], "encrypted_content": "e<>"},
    {"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{ }"},
    {"type": "function_call_output", "call_id": "c1", "output": "` + odd + `"},
    {"type": "custom_tool_call", "call_id": "c2", "name": "g", "input": "x"},
    {"type": "web_search_call", "id": "w1", "status": "completed", "action": { "type" : "search" }}
  ],
  "tools": [{"type": "function", "name": "f", "parameters": { "type": "object" }}, {"type": "web_search"}, {"type": "custom", "name": "g"}],
  "reasoning": {"effort": "high", "summary": "auto"}, "text": {"verbosity": "low"}, "prompt_cache_key": "k"
}`)},
		{src: "edge cases", name: "messages edge", path: "/v1/messages", headers: map[string]string{"Anthropic-Version": "2023-06-01", "Anthropic-Beta": "b1"}, body: []byte(`{
  "model": "m", "max_tokens": 10, "metadata": {"user_id": "u"},
  "system": [{"type": "text", "text": "` + odd + `", "cache_control": {"type": "ephemeral"}}],
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "` + odd + `"}, {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}]},
    {"role": "assistant", "content": [{"type": "thinking", "thinking": "t", "signature": "s<>"}, {"type": "redacted_thinking", "data": "d"}, {"type": "tool_use", "id": "t1", "name": "f", "input": { "a" : [1, 2] }}]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": [{"type": "text", "text": "` + odd + `"}], "is_error": true}]}
  ],
  "tools": [{"name": "f", "description": "d", "input_schema": { "type" : "object" }}, {"type": "web_search_20250305", "name": "web_search"}],
  "tool_choice": {"type": "auto", "disable_parallel_tool_use": true},
  "thinking": {"type": "enabled", "budget_tokens": 1024}, "stream": true
}`)},
		{src: "edge cases", name: "gemini edge", path: "/v1beta/models/g:streamGenerateContent", body: []byte(`{
  "systemInstruction": {"parts": [{"text": "` + odd + `"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "` + odd + `"}, {"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}}]},
    {"role": "model", "parts": [{"functionCall": {"name": "f", "args": { "a" : 1 }}, "thoughtSignature": "sig<>"}]},
    {"role": "user", "parts": [{"functionResponse": {"name": "f", "response": { "r" : "` + odd + `" }}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "f", "parameters": { "type": "OBJECT" }}]}, {"googleSearch": {}}],
  "toolConfig": {"functionCallingConfig": {"mode": "ANY"}, "x": 1},
  "generationConfig": {"temperature": 0.1, "thinkingConfig": {"thinkingBudget": 100}, "responseSchema": { "type": "OBJECT" }},
  "safetySettings": [{"category": "c", "threshold": "t"}]
}`)},
	}
}
