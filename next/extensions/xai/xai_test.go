package main

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func sameJSON(t *testing.T, what string, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v: %s", what, err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

// responsesBody builds the body for a Responses client request, the way the Responses format
// reads it, and returns the body the extension sends.
func responsesBody(t *testing.T, body string) []byte {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStreamFlagFollowsClient(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[{"role":"user","content":"hi"}]}`)
	sameJSON(t, "non-stream", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[{"role":"user","content":"hi"}]}`)
	got = responsesBody(t, `{"model":"grok-4.6","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	sameJSON(t, "stream", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":true,"input":[{"role":"user","content":"hi"}]}`)
}

func TestRemovedFields(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"stop":["x"],"previous_response_id":"resp_1",
		"prompt_cache_retention":"24h","safety_identifier":"u","stream_options":{"include_usage":true},"temperature":0.2}`)
	sameJSON(t, "removed fields", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"temperature":0.2}`)
}

// Responses are not kept on xAI unless the client asks, and encrypted reasoning is asked for
// alongside the client's own include values.
func TestStoreAndInclude(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"store":true,"include":["web_search_call.action.sources"]}`)
	sameJSON(t, "client store and include", got, `{"model":"grok-4.6","stream":false,"input":[],"store":true,
		"include":["web_search_call.action.sources","reasoning.encrypted_content"]}`)
	got = responsesBody(t, `{"model":"grok-4.6","input":[],"include":["reasoning.encrypted_content"]}`)
	sameJSON(t, "include once", got, `{"model":"grok-4.6","stream":false,"input":[],"store":false,"include":["reasoning.encrypted_content"]}`)
}

func TestCustomToolBecomesFunction(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[
		{"type":"custom","name":"run_shell","description":"run","format":{"type":"grammar","syntax":"lark","definition":"x"}}]}`)
	sameJSON(t, "custom tool", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"tools":[
		{"type":"function","name":"run_shell","description":"run","format":{"type":"grammar","syntax":"lark","definition":"x"},"parameters":{"type":"object","properties":{}}}]}`)
}

func TestFunctionSchemaInlined(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","$defs":{"C":{"type":"string","enum":["a","b"]}},"properties":{"c":{"$ref":"#/$defs/C"}}}}]}`)
	sameJSON(t, "schema", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","properties":{"c":{"type":"string","enum":["a","b"]}}}}]}`)
	// A definitions block is removed too, and an $ref in an array is inlined.
	got = responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","definitions":{"N":{"type":"integer"}},"properties":{"xs":{"type":"array","items":{"$ref":"#/definitions/N"}}}}}]}`)
	if strings.Contains(string(got), "definitions") || strings.Contains(string(got), "$ref") {
		t.Errorf("definitions or $ref left: %s", got)
	}
	if !strings.Contains(string(got), `"items":{"type":"integer"}`) {
		t.Errorf("array $ref not inlined: %s", got)
	}
}

func TestToolLimit(t *testing.T) {
	var tools []string
	for i := 0; i < maxTools+5; i++ {
		tools = append(tools, `{"type":"function","name":"t`+strings.Repeat("x", i%3)+`"}`)
	}
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[`+strings.Join(tools, ",")+`]}`)
	var m struct {
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(got, &m)
	if len(m.Tools) != maxTools {
		t.Fatalf("tools = %d, want %d", len(m.Tools), maxTools)
	}
	// A function without parameters gets the empty object schema, as in the core.
	var first struct {
		Parameters map[string]any `json:"parameters"`
	}
	_ = json.Unmarshal(m.Tools[0], &first)
	if !reflect.DeepEqual(first.Parameters, map[string]any{"type": "object", "properties": map[string]any{}}) {
		t.Errorf("first tool parameters = %v", first.Parameters)
	}
}

func TestCustomToolItems(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[
		{"type":"custom_tool_call","call_id":"c1","name":"run_shell","input":"ls -la"},
		{"type":"custom_tool_call_output","call_id":"c1","output":"done"},
		{"type":"custom_tool_call","call_id":"c2","name":"f","input":{"path":"a"}},
		{"type":"custom_tool_call","call_id":"","name":"f","input":"x"}]}`)
	sameJSON(t, "custom items", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[
		{"type":"function_call","call_id":"c1","name":"run_shell","arguments":"{\"input\":\"ls -la\"}"},
		{"type":"function_call_output","call_id":"c1","output":"done"},
		{"type":"function_call","call_id":"c2","name":"f","arguments":"{\"path\":\"a\"}"}]}`)
}

func TestChatClientTranslatedToResponses(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"grok-4.6","stream":true,"reasoning_effort":"high",
		"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if tr.To != llm.FormatOpenAIResponses || tr.Passthrough {
		t.Errorf("translation = %+v", tr)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["stream"] != true || m["model"] != "grok-4.6" {
		t.Errorf("body = %s", out)
	}
	if r, _ := m["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Errorf("reasoning = %v, want effort high", m["reasoning"])
	}
}

func errorFor(status int, header http.Header, body string) *llm.Error {
	resp := &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
	return upstreamError(resp, "grok-4.6")
}

func TestErrorMapping(t *testing.T) {
	none := http.Header{}
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		code   string
		scope  string
		retry  bool
		ms     int64
	}{
		{"invalid key", 401, none, `{"code":"Client specified an invalid argument","error":"Incorrect API key provided."}`, llm.CodeAuth, llm.ScopeCredential, true, 0},
		{"bad credentials on 403", 403, none, `{"error":{"message":"access token could not be validated","code":"bad-credentials"}}`, llm.CodeAuth, llm.ScopeCredential, true, 0},
		{"other 403", 403, none, `{"error":{"message":"The team has no access to this model","type":"permission_error"}}`, llm.CodePermission, llm.ScopeCredential, true, 0},
		{"free usage used up", 429, none, `{"code":"subscription:free-usage-exhausted","error":"included free usage used"}`, llm.CodeQuota, llm.ScopeCredential, true, 24 * 60 * 60 * 1000},
		{"rate limit with Retry-After", 429, http.Header{"Retry-After": {"20"}}, `{"code":"Too many requests","error":"Rate limit reached"}`, llm.CodeRateLimit, llm.ScopeCredential, true, 20000},
		{"model not found", 404, none, `{"error":{"message":"The model grok-4.6 does not exist"}}`, llm.CodeNotFound, llm.ScopeModel, true, 0},
		{"unavailable", 503, http.Header{"Retry-After": {"5"}}, `{"error":"overloaded"}`, llm.CodeUnavailable, llm.ScopeEndpoint, false, 5000},
		{"server error", 500, none, `oops`, llm.CodeUpstream, llm.ScopeEndpoint, false, 0},
		{"bad request", 400, none, `{"error":{"message":"bad input"}}`, llm.CodeInvalidRequest, llm.ScopeRequest, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := errorFor(c.status, c.header, c.body)
			if e.Code != c.code || e.Scope != c.scope || e.Retryable != c.retry || e.RetryAfterMS != c.ms {
				t.Errorf("got code=%s scope=%s retryable=%v retry_after=%d, want code=%s scope=%s retryable=%v retry_after=%d",
					e.Code, e.Scope, e.Retryable, e.RetryAfterMS, c.code, c.scope, c.retry, c.ms)
			}
		})
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if got := retryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90000 {
		t.Errorf("retryAfter(date) = %d, want 90000", got)
	}
	if got := retryAfter("soon", now); got != 0 {
		t.Errorf("retryAfter(bad) = %d, want 0", got)
	}
}

func TestSettingsDefaultBaseURL(t *testing.T) {
	s, err := readSettings(nil)
	if err != nil || s.BaseURL != "https://api.x.ai/v1" {
		t.Errorf("default = %+v, %v", s, err)
	}
	s, err = readSettings(json.RawMessage(`{"base_url":"http://127.0.0.1:9/v1/"}`))
	if err != nil || s.BaseURL != "http://127.0.0.1:9/v1" {
		t.Errorf("trimmed = %+v, %v", s, err)
	}
}
