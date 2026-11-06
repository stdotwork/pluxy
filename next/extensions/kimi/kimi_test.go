package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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

func TestUpstreamModel(t *testing.T) {
	for in, want := range map[string]string{
		"kimi-k3": "k3", "kimi-k2.8": "kimi-for-coding", "k2.7-code-highspeed": "kimi-for-coding-highspeed",
		"kimi-k3[1m]": "k3", "moonshot-v1": "moonshot-v1", "KIMI-K2.8-CODE": "kimi-for-coding",
	} {
		if got := upstreamModel(in); got != want {
			t.Errorf("upstreamModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func chatBody(t *testing.T, body string) []byte {
	t.Helper()
	req, err := openaichat.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := buildBody(req, upChat)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChatRequest(t *testing.T) {
	got := chatBody(t, `{"model":"kimi-k3","stream":true,"temperature":0.3,"reasoning_effort":"xhigh",
		"tools":[{"type":"function","function":{"name":"f","parameters":{"$defs":{"C":{"type":"string"}},"properties":{"c":{"$ref":"#/$defs/C"}}}}}],
		"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":""},
		{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	sameJSON(t, "chat body", got, `{"model":"k3","stream":true,"stream_options":{"include_usage":true},
		"thinking":{"type":"enabled","effort":"max"},
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"c":{"type":"string"}}}}}],
		"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":"Checking.","reasoning_content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)

	// Thinking off keeps 0.6 and nothing else; kimi-k2 gets no thinking settings.
	got = chatBody(t, `{"model":"kimi-k3","reasoning_effort":"none","temperature":0.6,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(string(got), `"temperature":0.6`) || !strings.Contains(string(got), `"thinking":{"type":"disabled"}`) {
		t.Errorf("thinking off: %s", got)
	}
	got = chatBody(t, `{"model":"kimi-k2","reasoning_effort":"high","temperature":1,"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(string(got), "thinking") || !strings.Contains(string(got), `"reasoning_effort":"high"`) || !strings.Contains(string(got), `"temperature":1`) {
		t.Errorf("kimi-k2: %s", got)
	}
}

func TestReasoningCarriesToLaterToolCalls(t *testing.T) {
	got := chatBody(t, `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":"a","reasoning_content":"thought it through"},
		{"role":"user","content":"go"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
	var m struct{ Messages []map[string]any }
	_ = json.Unmarshal(got, &m)
	if r := m.Messages[3]["reasoning_content"]; r != "thought it through" {
		t.Errorf("tool call reasoning_content = %v, want the latest reasoning", r)
	}
}

func TestResponsesOutputsContiguous(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"kimi-k3","input":[
		{"role":"user","content":"q"},
		{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},
		{"type":"function_call","call_id":"b","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"a","output":"1"},
		{"role":"developer","content":"note"},
		{"type":"function_call_output","call_id":"b","output":"2"},
		{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},
		{"role":"user","content":"no output for c yet"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := buildBody(req, upResponses)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Input []struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			CallID string `json:"call_id"`
		} `json:"input"`
		Stream *bool `json:"stream"`
	}
	_ = json.Unmarshal(got, &m)
	var order []string
	for _, it := range m.Input {
		order = append(order, it.Type+it.Role+":"+it.CallID)
	}
	want := []string{"user:", "function_call:a", "function_call:b", "function_call_output:a", "function_call_output:b", "developer:", "function_call:c", "user:"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if m.Stream == nil || *m.Stream {
		t.Error("a non-streamed Responses request does not say stream false")
	}
}

func TestBillingBlockRemoved(t *testing.T) {
	m := map[string]any{"system": []any{
		map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.0;"},
		map[string]any{"type": "text", "text": "Be kind."}}}
	stripBillingBlock(m)
	if sys := m["system"].([]any); len(sys) != 1 || sys[0].(map[string]any)["text"] != "Be kind." {
		t.Errorf("system = %v", m["system"])
	}
}

func TestUpstreamFor(t *testing.T) {
	for f, want := range map[string]upstream{llm.FormatOpenAIChat: upChat, llm.FormatGeminiGenerate: upChat,
		llm.FormatOpenAIResponses: upResponses, llm.FormatAnthropicMessages: upMessages} {
		if got := upstreamFor(llm.Request{Native: &llm.Native{Format: f}}); got != want {
			t.Errorf("%s goes to %v, want %v", f, got, want)
		}
	}
}
