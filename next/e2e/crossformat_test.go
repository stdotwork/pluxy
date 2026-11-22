package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type crossFormatSSE struct {
	Event string
	Data  string // JSON string or "[DONE]"
}

type crossFormatCase struct {
	extension      string // "openai-compatible", "claude", "gemini"
	wireAPI        string // optional, for openai-compatible: "", "responses", "messages", "gemini"
	model          string
	clientPath     string
	clientHeaders  map[string]string
	clientNoBearer bool // when true, do not set Authorization: Bearer <clientKey>
	clientBody     string

	upstreamStatus int // default 200
	upstreamBody   string
	upstreamSSE    []crossFormatSSE

	wantUpstreamPath   string
	wantUpstreamHeader map[string]string
	wantUpstreamBody   string

	wantClientStatus int // default 200
	wantClientBody   string
	wantClientSSE    []crossFormatSSE
	normalizeClient  func(v any)
}

func runCrossFormatCase(t *testing.T, tc crossFormatCase) {
	t.Helper()

	type seenReq struct {
		method  string
		path    string
		headers http.Header
		body    []byte
	}
	var (
		mu   sync.Mutex
		seen []seenReq
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenReq{
			method:  r.Method,
			path:    r.URL.RequestURI(),
			headers: r.Header.Clone(),
			body:    body,
		})
		mu.Unlock()

		status := tc.upstreamStatus
		if status == 0 {
			status = http.StatusOK
		}
		if len(tc.upstreamSSE) > 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(status)
			for _, ev := range tc.upstreamSSE {
				if ev.Event != "" {
					fmt.Fprintf(w, "event: %s\n", ev.Event)
				}
				fmt.Fprintf(w, "data: %s\n\n", ev.Data)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, tc.upstreamBody)
	}))
	t.Cleanup(up.Close)

	var manifest string
	settings := map[string]any{}
	switch tc.extension {
	case "openai-compatible":
		manifest = filepath.Join(binDir, "pluxy-extension.json")
		switch tc.wireAPI {
		case "":
			settings["base_url"] = up.URL + "/v1"
		case "responses", "messages":
			settings["base_url"] = up.URL + "/v1"
			settings["wire_api"] = tc.wireAPI
		case "gemini":
			settings["base_url"] = up.URL + "/v1beta"
			settings["wire_api"] = "gemini"
		default:
			t.Fatalf("unknown wire_api %q", tc.wireAPI)
		}
	case "claude":
		manifest = filepath.Join(binDir, "claude", "pluxy-extension.json")
		settings["base_url"] = up.URL
	case "gemini":
		manifest = filepath.Join(binDir, "gemini", "pluxy-extension.json")
		settings["base_url"] = up.URL
	default:
		t.Fatalf("unknown extension %q", tc.extension)
	}

	acct := map[string]any{
		"id":         "crossformat",
		"models":     []string{tc.model},
		"settings":   settings,
		"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
	}
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": manifest,
			"accounts": []any{acct},
		},
	})

	req, err := http.NewRequest(http.MethodPost, base+tc.clientPath, strings.NewReader(tc.clientBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if !tc.clientNoBearer {
		req.Header.Set("Authorization", "Bearer "+clientKey)
	}
	for k, v := range tc.clientHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rawReply, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	wantStatus := tc.wantClientStatus
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("client status = %d, want %d; body: %s", resp.StatusCode, wantStatus, rawReply)
	}

	mu.Lock()
	reqs := append([]seenReq(nil), seen...)
	mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(reqs))
	}
	gotReq := reqs[0]
	if gotReq.method != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotReq.method)
	}
	if gotReq.path != tc.wantUpstreamPath {
		t.Errorf("upstream path = %q, want %q", gotReq.path, tc.wantUpstreamPath)
	}
	for k, want := range tc.wantUpstreamHeader {
		if got := gotReq.headers.Get(k); got != want {
			t.Errorf("upstream header %s = %q, want %q", k, got, want)
		}
	}
	if tc.wantUpstreamBody != "" {
		assertJSONEqual(t, "upstream body", gotReq.body, []byte(tc.wantUpstreamBody), nil)
	}

	if len(tc.wantClientSSE) > 0 {
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("client Content-Type = %q, want text/event-stream", ct)
		}
		gotEvents := parseSSEStream(t, rawReply)
		if len(gotEvents) != len(tc.wantClientSSE) {
			t.Fatalf("client SSE event count = %d, want %d\ngot: %s", len(gotEvents), len(tc.wantClientSSE), rawReply)
		}
		for i, want := range tc.wantClientSSE {
			got := gotEvents[i]
			if got.Event != want.Event {
				t.Errorf("client SSE[%d].event = %q, want %q", i, got.Event, want.Event)
			}
			if want.Data == "[DONE]" {
				if got.Data != "[DONE]" {
					t.Errorf("client SSE[%d].data = %q, want [DONE]", i, got.Data)
				}
				continue
			}
			assertJSONEqual(t, fmt.Sprintf("client SSE[%d].data", i), []byte(got.Data), []byte(want.Data), tc.normalizeClient)
		}
	} else if tc.wantClientBody != "" {
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("client Content-Type = %q, want application/json", ct)
		}
		assertJSONEqual(t, "client body", rawReply, []byte(tc.wantClientBody), tc.normalizeClient)
	}
}

func assertJSONEqual(t *testing.T, label string, gotRaw, wantRaw []byte, normalize func(any)) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatalf("%s: unmarshal got (%s): %v", label, gotRaw, err)
	}
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatalf("%s: unmarshal want (%s): %v", label, wantRaw, err)
	}
	if normalize != nil {
		normalize(got)
	}
	if !reflect.DeepEqual(got, want) {
		gotPretty, _ := json.MarshalIndent(got, "", "  ")
		wantPretty, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("%s mismatch:\ngot:\n%s\nwant:\n%s", label, gotPretty, wantPretty)
	}
}

func parseSSEStream(t *testing.T, raw []byte) []crossFormatSSE {
	t.Helper()
	var out []crossFormatSSE
	var cur crossFormatSSE
	var dataLines []string
	flush := func() {
		if cur.Event == "" && len(dataLines) == 0 {
			return
		}
		cur.Data = strings.Join(dataLines, "\n")
		out = append(out, cur)
		cur = crossFormatSSE{}
		dataLines = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, val, _ := strings.Cut(line, ":")
		val = strings.TrimPrefix(val, " ")
		switch field {
		case "event":
			cur.Event = val
		case "data":
			dataLines = append(dataLines, val)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}
	flush()
	return out
}

// A Messages history with tool_use and a tool_result holding an image beside its text reaches
// Gemini as functionCall and functionResponse turns, with the image in functionResponse.parts as
// inlineData, and the Gemini stream comes back as Messages SSE events.
func TestCrossFormatMessagesToGeminiToolResultImage(t *testing.T) {
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	runCrossFormatCase(t, crossFormatCase{
		extension:  "gemini",
		model:      "gemini-corpus",
		clientPath: "/v1/messages",
		clientBody: `{
			"model": "gemini-corpus",
			"stream": true,
			"max_tokens": 512,
			"tools": [{
				"name": "get_weather",
				"description": "Weather for a city",
				"input_schema": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"]
				}
			}],
			"messages": [
				{"role": "user", "content": "What is the weather in Oslo?"},
				{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Oslo"}}]},
				{"role": "user", "content": [{
					"type": "tool_result",
					"tool_use_id": "toolu_1",
					"content": [
						{"type": "text", "text": "{\"temp\":12}"},
						{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}}
					]
				}]}
			]
		}`,
		upstreamSSE: []crossFormatSSE{
			{Data: `{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking...","thought":true}]},"index":0}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus"}`},
			{Data: `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus"}`},
			{Data: `{"candidates":[{"content":{"role":"model","parts":[{"text":" world"},{"text":"","thoughtSignature":"CiQB0e2Kb7sigA1"}]},"index":0}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus"}`},
			{Data: `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"index":0,"finishReason":"STOP"}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus","usageMetadata":{"promptTokenCount":40,"cachedContentTokenCount":8,"candidatesTokenCount":12,"thoughtsTokenCount":20,"totalTokenCount":72}}`},
		},
		wantUpstreamPath: "/v1beta/models/gemini-corpus:streamGenerateContent?alt=sse",
		wantUpstreamHeader: map[string]string{
			"X-Goog-Api-Key": upstreamKey,
			"Accept":         "text/event-stream",
		},
		wantUpstreamBody: `{
			"contents": [
				{"role": "user", "parts": [{"text": "What is the weather in Oslo?"}]},
				{"role": "model", "parts": [{"functionCall": {"args": {"city": "Oslo"}, "id": "toolu_1", "name": "get_weather"}, "thoughtSignature": "skip_thought_signature_validator"}]},
				{"role": "user", "parts": [{"functionResponse": {"id": "toolu_1", "name": "get_weather", "response": {"result": "{\"temp\":12}"}, "parts": [{"inlineData": {"mimeType": "image/png", "data": "` + pngB64 + `"}}]}}]}
			],
			"model": "gemini-corpus",
			"safetySettings": [
				{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"}
			],
			"tools": [{
				"functionDeclarations": [{
					"description": "Weather for a city",
					"name": "get_weather",
					"parametersJsonSchema": {
						"type": "object",
						"properties": {"city": {"type": "string"}},
						"required": ["city"]
					}
				}]
			}]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"message":{"content":[],"id":"resp-corpus","model":"gemini-corpus-upstream","role":"assistant","stop_reason":null,"stop_sequence":null,"type":"message","usage":{"input_tokens":0,"output_tokens":0}},"type":"message_start"}`},
			{Event: "content_block_start", Data: `{"content_block":{"thinking":"","type":"thinking"},"index":0,"type":"content_block_start"}`},
			{Event: "content_block_delta", Data: `{"delta":{"thinking":"thinking...","type":"thinking_delta"},"index":0,"type":"content_block_delta"}`},
			{Event: "content_block_stop", Data: `{"index":0,"type":"content_block_stop"}`},
			{Event: "content_block_start", Data: `{"content_block":{"text":"","type":"text"},"index":1,"type":"content_block_start"}`},
			{Event: "content_block_delta", Data: `{"delta":{"text":"Hello","type":"text_delta"},"index":1,"type":"content_block_delta"}`},
			{Event: "content_block_delta", Data: `{"delta":{"text":" world","type":"text_delta"},"index":1,"type":"content_block_delta"}`},
			{Event: "content_block_stop", Data: `{"index":1,"type":"content_block_stop"}`},
			{Event: "content_block_start", Data: `{"content_block":{"thinking":"","type":"thinking"},"index":2,"type":"content_block_start"}`},
			{Event: "content_block_delta", Data: `{"delta":{"signature":"CiQB0e2Kb7sigA1","type":"signature_delta"},"index":2,"type":"content_block_delta"}`},
			{Event: "content_block_stop", Data: `{"index":2,"type":"content_block_stop"}`},
			{Event: "message_delta", Data: `{"delta":{"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta","usage":{"cache_read_input_tokens":8,"input_tokens":32,"output_tokens":32,"output_tokens_details":{"thinking_tokens":20}}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
	})
}

func normalizeVolatileFields(keys ...string) func(any) {
	return func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, k := range keys {
			switch k {
			case "created":
				if _, ok := m["created"]; ok {
					m["created"] = "(volatile)"
				}
			case "created_at":
				if _, ok := m["created_at"]; ok {
					m["created_at"] = "(volatile)"
				}
			case "response.created_at":
				if resp, ok := m["response"].(map[string]any); ok {
					if _, ok := resp["created_at"]; ok {
						resp["created_at"] = "(volatile)"
					}
				}
			case "choices[*].delta.tool_calls[*].id":
				choices, _ := m["choices"].([]any)
				for _, ch := range choices {
					cm, _ := ch.(map[string]any)
					delta, _ := cm["delta"].(map[string]any)
					tcs, _ := delta["tool_calls"].([]any)
					for _, tc := range tcs {
						tm, _ := tc.(map[string]any)
						if id, ok := tm["id"].(string); ok && id != "" {
							tm["id"] = "(volatile)"
						}
					}
				}
			}
		}
	}
}

// A Messages request with output_config.format json_schema translates to Chat response_format
// json_schema for an OpenAI-compatible upstream, and the Chat reply comes back as a Messages body.
func TestCrossFormatMessagesToChatOutputConfigFormat(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:      "openai-compatible",
		model:          "gpt-test",
		clientPath:     "/v1/messages",
		clientNoBearer: true,
		clientHeaders:  map[string]string{"x-api-key": clientKey, "anthropic-version": "2023-06-01"},
		clientBody: `{
			"model": "gpt-test",
			"max_tokens": 256,
			"messages": [{"role": "user", "content": "Say hello"}],
			"output_config": {
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"additionalProperties": false
					}
				}
			}
		}`,
		upstreamBody: `{
			"id": "chatcmpl-corpus",
			"object": "chat.completion",
			"created": 1700000001,
			"model": "gpt-test-upstream",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello there."}, "finish_reason": "stop"}],
			"usage": {
				"prompt_tokens": 120,
				"completion_tokens": 6,
				"total_tokens": 126,
				"prompt_tokens_details": {"cached_tokens": 100},
				"completion_tokens_details": {"reasoning_tokens": 2}
			}
		}`,
		wantUpstreamPath:   "/v1/chat/completions",
		wantUpstreamHeader: map[string]string{"Authorization": "Bearer " + upstreamKey},
		wantUpstreamBody: `{
			"max_tokens": 256,
			"messages": [{"content": "Say hello", "role": "user"}],
			"model": "gpt-test",
			"response_format": {
				"type": "json_schema",
				"json_schema": {
					"name": "response",
					"schema": {
						"type": "object",
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"additionalProperties": false
					}
				}
			},
			"stream": false
		}`,
		wantClientBody: `{
			"content": [{"text": "Hello there.", "type": "text"}],
			"id": "chatcmpl-corpus",
			"model": "gpt-test-upstream",
			"role": "assistant",
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"type": "message",
			"usage": {
				"cache_read_input_tokens": 100,
				"input_tokens": 20,
				"output_tokens": 6,
				"output_tokens_details": {"thinking_tokens": 2}
			}
		}`,
	})
}

// A Messages follow-up turn with out-of-order tool results where one tool_result holds an image
// beside its text reaches a Chat upstream with one tool message per result in client order,
// followed by a user message carrying the tool image and the user's text.
func TestCrossFormatMessagesToChatToolResultImage(t *testing.T) {
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	runCrossFormatCase(t, crossFormatCase{
		extension:      "openai-compatible",
		model:          "gpt-test",
		clientPath:     "/v1/messages",
		clientNoBearer: true,
		clientHeaders:  map[string]string{"x-api-key": clientKey, "anthropic-version": "2023-06-01"},
		clientBody: `{
			"model": "gpt-test",
			"max_tokens": 512,
			"tools": [{
				"name": "get_weather",
				"description": "Get the current weather for a city.",
				"input_schema": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"]
				}
			}],
			"messages": [
				{"role": "user", "content": "What is the weather in Paris and Rome?"},
				{"role": "assistant", "content": [
					{"type": "text", "text": "Checking."},
					{"type": "tool_use", "id": "toolu_01A", "name": "get_weather", "input": {"city": "Paris"}},
					{"type": "tool_use", "id": "toolu_01B", "name": "get_weather", "input": {"city": "Rome"}}
				]},
				{"role": "user", "content": [
					{"type": "tool_result", "tool_use_id": "toolu_01B", "content": "18C"},
					{"type": "tool_result", "tool_use_id": "toolu_01A", "content": [
						{"type": "text", "text": "21C"},
						{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}}
					]},
					{"type": "text", "text": "Which is warmer?"}
				]}
			]
		}`,
		upstreamBody: `{
			"id": "chatcmpl-corpus",
			"object": "chat.completion",
			"created": 1700000001,
			"model": "gpt-test-upstream",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Paris is warmer, at 21C, than Rome at 18C."}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 90, "completion_tokens": 12, "total_tokens": 102}
		}`,
		wantUpstreamPath:   "/v1/chat/completions",
		wantUpstreamHeader: map[string]string{"Authorization": "Bearer " + upstreamKey},
		wantUpstreamBody: `{
			"max_tokens": 512,
			"messages": [
				{"content": "What is the weather in Paris and Rome?", "role": "user"},
				{
					"content": [{"text": "Checking.", "type": "text"}],
					"role": "assistant",
					"tool_calls": [
						{"function": {"arguments": "{\"city\":\"Paris\"}", "name": "get_weather"}, "id": "toolu_01A", "type": "function"},
						{"function": {"arguments": "{\"city\":\"Rome\"}", "name": "get_weather"}, "id": "toolu_01B", "type": "function"}
					]
				},
				{"content": "18C", "role": "tool", "tool_call_id": "toolu_01B"},
				{"content": "21C", "role": "tool", "tool_call_id": "toolu_01A"},
				{"content": [
					{"type": "text", "text": "Images returned by the preceding tool call(s):"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + pngB64 + `"}},
					{"type": "text", "text": "Which is warmer?"}
				], "role": "user"}
			],
			"model": "gpt-test",
			"stream": false,
			"tools": [{
				"function": {
					"description": "Get the current weather for a city.",
					"name": "get_weather",
					"parameters": {
						"properties": {"city": {"type": "string"}},
						"required": ["city"],
						"type": "object"
					}
				},
				"type": "function"
			}]
		}`,
		wantClientBody: `{
			"content": [{"text": "Paris is warmer, at 21C, than Rome at 18C.", "type": "text"}],
			"id": "chatcmpl-corpus",
			"model": "gpt-test-upstream",
			"role": "assistant",
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"type": "message",
			"usage": {"input_tokens": 90, "output_tokens": 12}
		}`,
	})
}

// A Messages follow-up turn whose tool_result holds an image beside its text reaches a Responses
// upstream as a function_call_output item whose output list carries both input_text and
// input_image, and the Responses stream comes back as Messages SSE events.
func TestCrossFormatMessagesToResponsesToolResultImage(t *testing.T) {
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	runCrossFormatCase(t, crossFormatCase{
		extension:      "openai-compatible",
		wireAPI:        "responses",
		model:          "gpt-test",
		clientPath:     "/v1/messages",
		clientNoBearer: true,
		clientHeaders:  map[string]string{"x-api-key": clientKey, "anthropic-version": "2023-06-01"},
		clientBody: `{
			"model": "gpt-test",
			"max_tokens": 1024,
			"stream": true,
			"tools": [{
				"name": "get_weather",
				"description": "Current weather",
				"input_schema": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"]
				}
			}],
			"messages": [
				{"role": "user", "content": "Weather in Paris?"},
				{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_01A", "name": "get_weather", "input": {"city": "Paris"}}]},
				{"role": "user", "content": [
					{"type": "tool_result", "tool_use_id": "toolu_01A", "content": [
						{"type": "text", "text": "21C"},
						{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}}
					]},
					{"type": "text", "text": "Thanks"}
				]}
			]
		}`,
		upstreamSSE: []crossFormatSSE{
			{Event: "response.created", Data: `{"type":"response.created","sequence_number":0,"response":{"id":"resp_corpus","object":"response","created_at":1700000000,"status":"in_progress","model":"gpt-test-upstream","output":[],"usage":null}}`},
			{Event: "response.output_item.added", Data: `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_corpus","type":"message","status":"in_progress","role":"assistant","content":[]}}`},
			{Event: "response.content_part.added", Data: `{"type":"response.content_part.added","sequence_number":2,"output_index":0,"content_index":0,"item_id":"msg_corpus","part":{"type":"output_text","text":"","annotations":[]}}`},
			{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"content_index":0,"item_id":"msg_corpus","delta":"You're"}`},
			{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","sequence_number":4,"output_index":0,"content_index":0,"item_id":"msg_corpus","delta":" welcome."}`},
			{Event: "response.output_text.done", Data: `{"type":"response.output_text.done","sequence_number":5,"output_index":0,"content_index":0,"item_id":"msg_corpus","text":"You're welcome."}`},
			{Event: "response.content_part.done", Data: `{"type":"response.content_part.done","sequence_number":6,"output_index":0,"content_index":0,"item_id":"msg_corpus","part":{"type":"output_text","text":"You're welcome.","annotations":[]}}`},
			{Event: "response.output_item.done", Data: `{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"id":"msg_corpus","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"You're welcome.","annotations":[]}]}}`},
			{Event: "response.completed", Data: `{"type":"response.completed","sequence_number":8,"response":{"id":"resp_corpus","object":"response","created_at":1700000000,"status":"completed","model":"gpt-test-upstream","output":[{"id":"msg_corpus","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"You're welcome.","annotations":[]}]}],"usage":{"input_tokens":40,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":44}}}`},
		},
		wantUpstreamPath: "/v1/responses",
		wantUpstreamHeader: map[string]string{
			"Authorization": "Bearer " + upstreamKey,
			"Accept":        "text/event-stream",
		},
		wantUpstreamBody: `{
			"input": [
				{"content": [{"text": "Weather in Paris?", "type": "input_text"}], "role": "user", "type": "message"},
				{"arguments": "{\"city\":\"Paris\"}", "call_id": "toolu_01A", "name": "get_weather", "type": "function_call"},
				{"call_id": "toolu_01A", "output": [
					{"type": "input_text", "text": "21C"},
					{"type": "input_image", "image_url": "data:image/png;base64,` + pngB64 + `", "detail": "auto"}
				], "type": "function_call_output"},
				{"content": [{"text": "Thanks", "type": "input_text"}], "role": "user", "type": "message"}
			],
			"max_output_tokens": 1024,
			"model": "gpt-test",
			"stream": true,
			"tools": [{
				"description": "Current weather",
				"name": "get_weather",
				"parameters": {
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"type": "object"
				},
				"type": "function"
			}]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"message":{"content":[],"id":"resp_corpus","model":"gpt-test-upstream","role":"assistant","stop_reason":null,"stop_sequence":null,"type":"message","usage":{"input_tokens":0,"output_tokens":0}},"type":"message_start"}`},
			{Event: "content_block_start", Data: `{"content_block":{"text":"","type":"text"},"index":0,"type":"content_block_start"}`},
			{Event: "content_block_delta", Data: `{"delta":{"text":"You're","type":"text_delta"},"index":0,"type":"content_block_delta"}`},
			{Event: "content_block_delta", Data: `{"delta":{"text":" welcome.","type":"text_delta"},"index":0,"type":"content_block_delta"}`},
			{Event: "content_block_stop", Data: `{"index":0,"type":"content_block_stop"}`},
			{Event: "message_delta", Data: `{"delta":{"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta","usage":{"input_tokens":40,"output_tokens":4,"output_tokens_details":{"thinking_tokens":0}}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
	})
}

// A Gemini request whose functionCallingConfig is ANY with two allowedFunctionNames reaches a Chat
// upstream as Chat's allowed_tools list with mode required and both functions declared, and the
// Chat tool-call stream comes back as a Gemini SSE chunk.
func TestCrossFormatGeminiToChatAllowedTools(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "openai-compatible",
		model:      "gpt-test",
		clientPath: "/v1beta/models/gpt-test:streamGenerateContent?alt=sse",
		clientBody: `{
			"contents": [{"role": "user", "parts": [{"text": "Weather in Paris?"}]}],
			"tools": [{
				"functionDeclarations": [
					{
						"name": "get_weather",
						"description": "Get the current weather for a city.",
						"parametersJsonSchema": {
							"type": "object",
							"properties": {"city": {"type": "string"}},
							"required": ["city"]
						}
					},
					{
						"name": "get_time",
						"description": "Current time in a city."
					}
				]
			}],
			"toolConfig": {
				"functionCallingConfig": {
					"mode": "ANY",
					"allowedFunctionNames": ["get_weather", "get_time"]
				}
			}
		}`,
		upstreamSSE: []crossFormatSSE{
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[{"index":0,"delta":{"role":"assistant","content":null},"finish_reason":null}]}`},
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`},
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`},
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":null}]}`},
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`},
			{Data: `{"id":"chatcmpl-corpus","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream","choices":[],"usage":{"prompt_tokens":85,"completion_tokens":18,"total_tokens":103,"prompt_tokens_details":{"cached_tokens":0}}}`},
			{Data: "[DONE]"},
		},
		wantUpstreamPath: "/v1/chat/completions",
		wantUpstreamHeader: map[string]string{
			"Authorization": "Bearer " + upstreamKey,
			"Accept":        "text/event-stream",
		},
		wantUpstreamBody: `{
			"messages": [{"content": "Weather in Paris?", "role": "user"}],
			"model": "gpt-test",
			"stream": true,
			"stream_options": {"include_usage": true},
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "required",
					"tools": [
						{"type": "function", "function": {"name": "get_weather"}},
						{"type": "function", "function": {"name": "get_time"}}
					]
				}
			},
			"tools": [
				{
					"function": {
						"description": "Get the current weather for a city.",
						"name": "get_weather",
						"parameters": {
							"properties": {"city": {"type": "string"}},
							"required": ["city"],
							"type": "object"
						}
					},
					"type": "function"
				},
				{
					"function": {
						"description": "Current time in a city.",
						"name": "get_time"
					},
					"type": "function"
				}
			]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Data: `{"candidates":[{"content":{"parts":[{"functionCall":{"args":{"city":"Paris"},"id":"call_abc","name":"get_weather"}}],"role":"model"},"finishReason":"STOP","index":0}],"modelVersion":"gpt-test-upstream","responseId":"chatcmpl-corpus","usageMetadata":{"candidatesTokenCount":18,"promptTokenCount":85,"totalTokenCount":103}}`},
		},
	})
}

// A non-streamed Chat request with response_format json_schema reaches a Claude upstream with
// output_config.format carrying the schema, and the reply comes back as a Chat completion.
func TestCrossFormatChatToClaudeResponseFormatJSONSchema(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "claude",
		model:      "claude-test",
		clientPath: "/v1/chat/completions",
		clientBody: `{
			"model": "claude-test",
			"messages": [{"role": "system", "content": "Be brief."}, {"role": "user", "content": "Say hello"}],
			"response_format": {
				"type": "json_schema",
				"json_schema": {
					"name": "greeting",
					"strict": true,
					"schema": {
						"type": "object",
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"additionalProperties": false
					}
				}
			},
			"stream": false
		}`,
		upstreamSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_corpus","type":"message","role":"assistant","content":[],"model":"claude-test-upstream","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":14,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`},
			{Event: "ping", Data: `{"type":"ping"}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		wantUpstreamPath: "/v1/messages?beta=true",
		wantUpstreamHeader: map[string]string{
			"X-Api-Key":         upstreamKey,
			"Anthropic-Version": "2023-06-01",
			"Accept":            "application/json",
			"Authorization":     "",
		},
		wantUpstreamBody: `{
			"max_tokens": 32000,
			"messages": [{"content": [{"cache_control": {"type": "ephemeral"}, "text": "Say hello", "type": "text"}], "role": "user"}],
			"model": "claude-test",
			"output_config": {
				"format": {
					"schema": {
						"additionalProperties": false,
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"type": "object"
					},
					"type": "json_schema"
				}
			},
			"stream": false,
			"system": [{"cache_control": {"type": "ephemeral"}, "text": "Be brief.", "type": "text"}]
		}`,
		wantClientBody: `{
			"choices": [{"finish_reason": "stop", "index": 0, "message": {"content": "Hello there", "role": "assistant"}}],
			"created": "(volatile)",
			"id": "msg_corpus",
			"model": "claude-test-upstream",
			"object": "chat.completion",
			"usage": {
				"completion_tokens": 4,
				"prompt_tokens": 14,
				"prompt_tokens_details": {"cached_tokens": 0},
				"total_tokens": 18
			}
		}`,
		normalizeClient: normalizeVolatileFields("created"),
	})
}

// A streamed Chat turn whose tool message holds an image beside its text reaches a Claude upstream
// as a Messages user tool_result block carrying both text and image blocks, and Claude's text
// stream comes back as Chat SSE chunks.
func TestCrossFormatChatToClaudeToolResultImage(t *testing.T) {
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	runCrossFormatCase(t, crossFormatCase{
		extension:  "claude",
		model:      "claude-test",
		clientPath: "/v1/chat/completions",
		clientBody: `{
			"model": "claude-test",
			"messages": [
				{"role": "user", "content": "What is the weather in Oslo?"},
				{
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "toolu_corpus",
						"type": "function",
						"function": {"name": "get_weather", "arguments": "{\"city\":\"Oslo\"}"}
					}]
				},
				{"role": "tool", "tool_call_id": "toolu_corpus", "content": [
					{"type": "text", "text": "4C, rain"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + pngB64 + `"}}
				]}
			],
			"tools": [{
				"type": "function",
				"function": {
					"name": "get_weather",
					"parameters": {
						"type": "object",
						"properties": {"city": {"type": "string"}},
						"required": ["city"]
					}
				}
			}],
			"stream": true
		}`,
		upstreamSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_corpus","type":"message","role":"assistant","content":[],"model":"claude-test-upstream","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":58,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`},
			{Event: "ping", Data: `{"type":"ping"}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is 4C and raining in Oslo."}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		wantUpstreamPath: "/v1/messages?beta=true",
		wantUpstreamHeader: map[string]string{
			"X-Api-Key":         upstreamKey,
			"Anthropic-Version": "2023-06-01",
			"Accept":            "text/event-stream",
			"Authorization":     "",
		},
		wantUpstreamBody: `{
			"max_tokens": 32000,
			"messages": [
				{"content": [{"text": "What is the weather in Oslo?", "type": "text"}], "role": "user"},
				{"content": [{"id": "toolu_corpus", "input": {"city": "Oslo"}, "name": "get_weather", "type": "tool_use"}], "role": "assistant"},
				{"content": [{
					"cache_control": {"type": "ephemeral"},
					"content": [
						{"type": "text", "text": "4C, rain"},
						{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}}
					],
					"tool_use_id": "toolu_corpus",
					"type": "tool_result"
				}], "role": "user"}
			],
			"model": "claude-test",
			"stream": true,
			"tools": [{
				"cache_control": {"type": "ephemeral"},
				"input_schema": {
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"type": "object"
				},
				"name": "get_weather"
			}]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Data: `{"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":null,"index":0}],"created":"(volatile)","id":"msg_corpus","model":"claude-test-upstream","object":"chat.completion.chunk"}`},
			{Data: `{"choices":[{"delta":{"content":"It is 4C and raining in Oslo."},"finish_reason":null,"index":0}],"created":"(volatile)","id":"msg_corpus","model":"claude-test-upstream","object":"chat.completion.chunk"}`},
			{Data: `{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"created":"(volatile)","id":"msg_corpus","model":"claude-test-upstream","object":"chat.completion.chunk"}`},
			{Data: "[DONE]"},
		},
		normalizeClient: normalizeVolatileFields("created"),
	})
}

// A streamed Chat request whose tool_choice is an allowed_tools list reaches Gemini as function
// calling mode ANY with both allowedFunctionNames and both tools declared, and Gemini's function
// call stream comes back as Chat SSE chunks.
func TestCrossFormatChatToGeminiAllowedTools(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "gemini",
		model:      "gemini-corpus",
		clientPath: "/v1/chat/completions",
		clientBody: `{
			"model": "gemini-corpus",
			"stream": true,
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "get_weather",
						"description": "Weather for a city",
						"parameters": {
							"type": "object",
							"properties": {"city": {"type": "string"}},
							"required": ["city"]
						}
					}
				},
				{
					"type": "function",
					"function": {
						"name": "get_time",
						"description": "Current time in a city",
						"parameters": {
							"type": "object",
							"properties": {"city": {"type": "string"}},
							"required": ["city"]
						}
					}
				}
			],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "required",
					"tools": [
						{"type": "function", "function": {"name": "get_weather"}},
						{"type": "function", "function": {"name": "get_time"}}
					]
				}
			},
			"messages": [{"role": "user", "content": "What is the weather in Oslo?"}]
		}`,
		upstreamSSE: []crossFormatSSE{
			{Data: `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Oslo"}},"thoughtSignature":"CiQB0e2Kb7sigA1"}]},"index":0}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus"}`},
			{Data: `{"candidates":[{"content":{"role":"model","parts":[]},"index":0,"finishReason":"STOP"}],"modelVersion":"gemini-corpus-upstream","responseId":"resp-corpus","usageMetadata":{"promptTokenCount":40,"cachedContentTokenCount":8,"candidatesTokenCount":12,"thoughtsTokenCount":20,"totalTokenCount":72}}`},
		},
		wantUpstreamPath: "/v1beta/models/gemini-corpus:streamGenerateContent?alt=sse",
		wantUpstreamHeader: map[string]string{
			"X-Goog-Api-Key": upstreamKey,
			"Accept":         "text/event-stream",
		},
		wantUpstreamBody: `{
			"contents": [{"parts": [{"text": "What is the weather in Oslo?"}], "role": "user"}],
			"model": "gemini-corpus",
			"safetySettings": [
				{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"}
			],
			"toolConfig": {
				"functionCallingConfig": {
					"allowedFunctionNames": ["get_weather", "get_time"],
					"mode": "ANY"
				}
			},
			"tools": [{
				"functionDeclarations": [
					{
						"description": "Weather for a city",
						"name": "get_weather",
						"parametersJsonSchema": {
							"properties": {"city": {"type": "string"}},
							"required": ["city"],
							"type": "object"
						}
					},
					{
						"description": "Current time in a city",
						"name": "get_time",
						"parametersJsonSchema": {
							"properties": {"city": {"type": "string"}},
							"required": ["city"],
							"type": "object"
						}
					}
				]
			}]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Data: `{"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":null,"index":0}],"created":"(volatile)","id":"resp-corpus","model":"gemini-corpus-upstream","object":"chat.completion.chunk"}`},
			{Data: `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"(volatile)","index":0,"type":"function"}]},"finish_reason":null,"index":0}],"created":"(volatile)","id":"resp-corpus","model":"gemini-corpus-upstream","object":"chat.completion.chunk"}`},
			{Data: `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"city\":\"Oslo\"}"},"index":0}]},"finish_reason":null,"index":0}],"created":"(volatile)","id":"resp-corpus","model":"gemini-corpus-upstream","object":"chat.completion.chunk"}`},
			{Data: `{"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"created":"(volatile)","id":"resp-corpus","model":"gemini-corpus-upstream","object":"chat.completion.chunk"}`},
			{Data: "[DONE]"},
		},
		normalizeClient: normalizeVolatileFields("created", "choices[*].delta.tool_calls[*].id"),
	})
}

// A Chat request with an input_audio part reaches a Gemini GenerateContent upstream (via the
// OpenAI-compatible extension's gemini wire) as inlineData with the audio MIME type its format
// names, and the Gemini reply comes back as a Chat completion.
func TestCrossFormatChatToGeminiWireInputAudio(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "openai-compatible",
		wireAPI:    "gemini",
		model:      "gemini-corpus",
		clientPath: "/v1/chat/completions",
		clientBody: `{
			"model": "gemini-corpus",
			"messages": [{
				"role": "user",
				"content": [
					{"type": "text", "text": "Say hello"},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			}]
		}`,
		upstreamBody: `{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "thinking...", "thought": true},
						{"text": "Hello world"}
					]
				},
				"finishReason": "STOP",
				"index": 0
			}],
			"usageMetadata": {
				"promptTokenCount": 40,
				"cachedContentTokenCount": 8,
				"candidatesTokenCount": 12,
				"thoughtsTokenCount": 20,
				"totalTokenCount": 72
			},
			"modelVersion": "gemini-corpus-upstream",
			"responseId": "resp-corpus"
		}`,
		wantUpstreamPath:   "/v1beta/models/gemini-corpus:generateContent",
		wantUpstreamHeader: map[string]string{"X-Goog-Api-Key": upstreamKey},
		wantUpstreamBody: `{
			"contents": [{
				"parts": [
					{"text": "Say hello"},
					{"inlineData": {"mimeType": "audio/mpeg", "data": "SUQzBA=="}}
				],
				"role": "user"
			}],
			"model": "gemini-corpus",
			"safetySettings": [
				{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
				{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"}
			]
		}`,
		wantClientBody: `{
			"choices": [{
				"finish_reason": "stop",
				"index": 0,
				"message": {
					"content": "Hello world",
					"reasoning_content": "thinking...",
					"role": "assistant"
				}
			}],
			"created": "(volatile)",
			"id": "resp-corpus",
			"model": "gemini-corpus-upstream",
			"object": "chat.completion",
			"usage": {
				"completion_tokens": 32,
				"completion_tokens_details": {"reasoning_tokens": 20},
				"prompt_tokens": 40,
				"prompt_tokens_details": {"cached_tokens": 8},
				"total_tokens": 72
			}
		}`,
		normalizeClient: normalizeVolatileFields("created"),
	})
}

// A streamed Responses request whose tool_choice is an allowed_tools list reaches a generic
// Messages upstream (openai-compatible wire_api messages) with only the allowed tool defined and
// tool_choice.type any, and the Responses stream echoes both tools and the allowed_tools choice.
func TestCrossFormatResponsesToMessagesWireAllowedTools(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "openai-compatible",
		wireAPI:    "messages",
		model:      "claude-test",
		clientPath: "/v1/responses",
		clientBody: `{
			"model": "claude-test",
			"instructions": "Be brief.",
			"input": "Say hello",
			"stream": true,
			"tools": [
				{
					"type": "function",
					"name": "get_weather",
					"parameters": {
						"type": "object",
						"properties": {"city": {"type": "string"}},
						"required": ["city"]
					}
				},
				{
					"type": "function",
					"name": "get_time",
					"parameters": {"type": "object"}
				}
			],
			"tool_choice": {
				"type": "allowed_tools",
				"mode": "required",
				"tools": [{"type": "function", "name": "get_weather"}]
			}
		}`,
		upstreamSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_corpus","type":"message","role":"assistant","content":[],"model":"claude-test-upstream","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":14,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`},
			{Event: "ping", Data: `{"type":"ping"}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		wantUpstreamPath: "/v1/messages",
		wantUpstreamHeader: map[string]string{
			"X-Api-Key":         upstreamKey,
			"Anthropic-Version": "2023-06-01",
			"Accept":            "text/event-stream",
			"Authorization":     "",
		},
		wantUpstreamBody: `{
			"max_tokens": 32000,
			"messages": [{"content": [{"text": "Say hello", "type": "text"}], "role": "user"}],
			"model": "claude-test",
			"stream": true,
			"system": [{"text": "Be brief.", "type": "text"}],
			"tool_choice": {"type": "any"},
			"tools": [{
				"input_schema": {
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"type": "object"
				},
				"name": "get_weather"
			}]
		}`,
		wantClientSSE: []crossFormatSSE{
			{Event: "response.created", Data: `{"response":{"background":false,"created_at":"(volatile)","error":null,"id":"msg_corpus","model":"claude-test","object":"response","output":[],"status":"in_progress"},"sequence_number":1,"type":"response.created"}`},
			{Event: "response.in_progress", Data: `{"response":{"created_at":"(volatile)","id":"msg_corpus","model":"claude-test","object":"response","output":[],"status":"in_progress"},"sequence_number":2,"type":"response.in_progress"}`},
			{Event: "response.output_item.added", Data: `{"item":{"content":[],"id":"msg_msg_corpus_0","role":"assistant","status":"in_progress","type":"message"},"output_index":0,"sequence_number":3,"type":"response.output_item.added"}`},
			{Event: "response.content_part.added", Data: `{"content_index":0,"item_id":"msg_msg_corpus_0","output_index":0,"part":{"annotations":[],"logprobs":[],"text":"","type":"output_text"},"sequence_number":4,"type":"response.content_part.added"}`},
			{Event: "response.output_text.delta", Data: `{"content_index":0,"delta":"Hello","item_id":"msg_msg_corpus_0","logprobs":[],"output_index":0,"sequence_number":5,"type":"response.output_text.delta"}`},
			{Event: "response.output_text.delta", Data: `{"content_index":0,"delta":" there","item_id":"msg_msg_corpus_0","logprobs":[],"output_index":0,"sequence_number":6,"type":"response.output_text.delta"}`},
			{Event: "response.output_text.done", Data: `{"content_index":0,"item_id":"msg_msg_corpus_0","logprobs":[],"output_index":0,"sequence_number":7,"text":"Hello there","type":"response.output_text.done"}`},
			{Event: "response.content_part.done", Data: `{"content_index":0,"item_id":"msg_msg_corpus_0","output_index":0,"part":{"annotations":[],"logprobs":[],"text":"Hello there","type":"output_text"},"sequence_number":8,"type":"response.content_part.done"}`},
			{Event: "response.output_item.done", Data: `{"item":{"content":[{"annotations":[],"logprobs":[],"text":"Hello there","type":"output_text"}],"id":"msg_msg_corpus_0","role":"assistant","status":"completed","type":"message"},"output_index":0,"sequence_number":9,"type":"response.output_item.done"}`},
			{Event: "response.completed", Data: `{"response":{"background":false,"created_at":"(volatile)","error":null,"id":"msg_corpus","instructions":"Be brief.","model":"claude-test","object":"response","output":[{"content":[{"annotations":[],"logprobs":[],"text":"Hello there","type":"output_text"}],"id":"msg_msg_corpus_0","role":"assistant","status":"completed","type":"message"}],"status":"completed","tool_choice":{"mode":"required","tools":[{"name":"get_weather","type":"function"}],"type":"allowed_tools"},"tools":[{"name":"get_weather","parameters":{"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"},"type":"function"},{"name":"get_time","parameters":{"type":"object"},"type":"function"}],"usage":{"input_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":18}},"sequence_number":10,"type":"response.completed"}`},
		},
		normalizeClient: normalizeVolatileFields("response.created_at"),
	})
}

// A non-streamed Responses request with text.format json_schema reaches a Claude upstream with
// output_config.format carrying the schema, and the Responses reply echoes text.format.
func TestCrossFormatResponsesToClaudeResponseFormatJSONSchema(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "claude",
		model:      "claude-test",
		clientPath: "/v1/responses",
		clientBody: `{
			"model": "claude-test",
			"instructions": "Be brief.",
			"input": "Say hello",
			"text": {
				"format": {
					"type": "json_schema",
					"name": "greeting",
					"strict": true,
					"schema": {
						"type": "object",
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"additionalProperties": false
					}
				}
			},
			"stream": false
		}`,
		upstreamSSE: []crossFormatSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_corpus","type":"message","role":"assistant","content":[],"model":"claude-test-upstream","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":14,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`},
			{Event: "ping", Data: `{"type":"ping"}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		wantUpstreamPath: "/v1/messages?beta=true",
		wantUpstreamHeader: map[string]string{
			"X-Api-Key":         upstreamKey,
			"Anthropic-Version": "2023-06-01",
			"Accept":            "application/json",
			"Authorization":     "",
		},
		wantUpstreamBody: `{
			"max_tokens": 32000,
			"messages": [{"content": [{"cache_control": {"type": "ephemeral"}, "text": "Say hello", "type": "text"}], "role": "user"}],
			"model": "claude-test",
			"output_config": {
				"format": {
					"schema": {
						"additionalProperties": false,
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"type": "object"
					},
					"type": "json_schema"
				}
			},
			"stream": false,
			"system": [{"cache_control": {"type": "ephemeral"}, "text": "Be brief.", "type": "text"}]
		}`,
		wantClientBody: `{
			"background": false,
			"created_at": "(volatile)",
			"error": null,
			"id": "msg_corpus",
			"incomplete_details": null,
			"instructions": "Be brief.",
			"model": "claude-test",
			"object": "response",
			"output": [{
				"content": [{"annotations": [], "logprobs": [], "text": "Hello there", "type": "output_text"}],
				"id": "msg_msg_corpus_0",
				"role": "assistant",
				"status": "completed",
				"type": "message"
			}],
			"status": "completed",
			"text": {
				"format": {
					"name": "greeting",
					"schema": {
						"additionalProperties": false,
						"properties": {"greeting": {"type": "string"}},
						"required": ["greeting"],
						"type": "object"
					},
					"strict": true,
					"type": "json_schema"
				}
			},
			"usage": {
				"input_tokens": 14,
				"input_tokens_details": {"cached_tokens": 0},
				"output_tokens": 4,
				"output_tokens_details": {"reasoning_tokens": 0},
				"total_tokens": 18
			}
		}`,
		normalizeClient: normalizeVolatileFields("created_at"),
	})
}

// A Responses request with an input_audio part beside an input_image reaches a Chat Completions
// upstream as Chat's input_audio part with data and format kept, and the Chat reply comes back
// as a Responses object.
func TestCrossFormatResponsesToChatInputAudio(t *testing.T) {
	runCrossFormatCase(t, crossFormatCase{
		extension:  "openai-compatible",
		model:      "gpt-test",
		clientPath: "/v1/responses",
		clientBody: `{
			"model": "gpt-test",
			"input": [{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "What is this?"},
					{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo=", "detail": "low"},
					{"type": "input_audio", "input_audio": {"data": "UklGRg==", "format": "wav"}}
				]
			}]
		}`,
		upstreamBody: `{
			"id": "chatcmpl-corpus",
			"object": "chat.completion",
			"created": 1700000001,
			"model": "gpt-test-upstream",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "A tiny image."}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}
		}`,
		wantUpstreamPath:   "/v1/chat/completions",
		wantUpstreamHeader: map[string]string{"Authorization": "Bearer " + upstreamKey},
		wantUpstreamBody: `{
			"messages": [{
				"content": [
					{"text": "What is this?", "type": "text"},
					{"image_url": {"detail": "low", "url": "data:image/png;base64,iVBORw0KGgo="}, "type": "image_url"},
					{"input_audio": {"data": "UklGRg==", "format": "wav"}, "type": "input_audio"}
				],
				"role": "user"
			}],
			"model": "gpt-test",
			"stream": false
		}`,
		wantClientBody: `{
			"background": false,
			"created_at": 1700000001,
			"error": null,
			"id": "chatcmpl-corpus",
			"incomplete_details": null,
			"model": "gpt-test",
			"object": "response",
			"output": [{
				"content": [{"annotations": [], "logprobs": [], "text": "A tiny image.", "type": "output_text"}],
				"id": "msg_chatcmpl-corpus_0",
				"role": "assistant",
				"status": "completed",
				"type": "message"
			}],
			"status": "completed",
			"usage": {
				"input_tokens": 4,
				"input_tokens_details": {"cached_tokens": 0},
				"output_tokens": 2,
				"output_tokens_details": {"reasoning_tokens": 0},
				"total_tokens": 6
			}
		}`,
	})
}
