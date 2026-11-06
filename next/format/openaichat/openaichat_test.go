package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	return b.String()
}

// A Chat request read into items and written back out keeps its conversation, with tool calls
// regrouped onto their assistant message.
func TestParseThenEncodeRequest(t *testing.T) {
	in := `{"model":"m","stream":true,"max_tokens":50,"temperature":0.5,"stop":"END",
	"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object"}}}],
	"messages":[
	 {"role":"system","content":"Be brief."},
	 {"role":"user","content":[{"type":"text","text":"Weather in Oslo and Bergen?"},{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}},{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]},
	 {"role":"assistant","content":"Checking.","tool_calls":[
	   {"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}},
	   {"id":"call_2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Bergen\"}"}}]},
	 {"role":"tool","tool_call_id":"call_1","content":"4C rain"},
	 {"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"6C sun"}]}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIChat || string(req.Native.Body) != in {
		t.Fatalf("native body not kept")
	}
	if got := len(req.Items); got != 7 {
		t.Fatalf("got %d items, want 7 (system, user, assistant, 2 tool calls, 2 tool results)", got)
	}
	if p := req.Items[1].Content[2]; p.Type != llm.PartNative {
		t.Fatalf("unknown part became %q, want native", p.Type)
	}
	out, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","messages":[
	 {"role":"system","content":"Be brief."},
	 {"role":"user","content":[{"type":"text","text":"Weather in Oslo and Bergen?"},{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}},{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]},
	 {"role":"assistant","content":"Checking.","tool_calls":[
	   {"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}},
	   {"id":"call_2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Bergen\"}"}}]},
	 {"role":"tool","content":"4C rain","tool_call_id":"call_1"},
	 {"role":"tool","content":"6C sun","tool_call_id":"call_2"}],
	 "stream":true,"stream_options":{"include_usage":true},"max_tokens":50,"temperature":0.5,"stop":["END"],
	 "tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object"}}}]}`
	if got, w := string(out), compactJSON(t, want); got != w {
		t.Errorf("encoded:\n got %s\nwant %s", got, w)
	}
}

func TestParseRequestRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":       `{`,
		"no model":       `{"messages":[{"role":"user","content":"hi"}]}`,
		"no messages":    `{"model":"m","messages":[]}`,
		"unknown role":   `{"model":"m","messages":[{"role":"wizard","content":"hi"}]}`,
		"non-text tool":  `{"model":"m","messages":[{"role":"tool","tool_call_id":"c","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		"bad stop":       `{"model":"m","stop":7,"messages":[{"role":"user","content":"hi"}]}`,
		"non-func tools": `{"model":"m","tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"hi"}]}`,
	} {
		_, err := ParseRequest([]byte(body))
		le, ok := err.(*llm.Error)
		if !ok || le.Code != llm.CodeInvalidRequest {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
}

// Streaming chunks shaped like OpenAI's documented streamed reasoning and parallel tool calls.
func TestStreamDecoder(t *testing.T) {
	head := `"id":"x","object":"chat.completion.chunk","created":5,"model":"up"`
	chunks := []string{
		`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":"Think"},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"reasoning_content":"ing."},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"g","arguments":"{\"y\""}}]},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{` + head + `,"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`[DONE]`,
	}
	d := NewStreamDecoder()
	var got []string
	var seq llm.Sequence
	var last llm.Event
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if err := seq.Check(ev); err != nil {
				t.Fatalf("decoder broke the event rules: %v", err)
			}
			s := ev.Type
			if ev.Item != nil {
				s += " " + ev.Item.Kind + " " + ev.Item.CallID
			}
			if ev.Delta != "" {
				s += " " + ev.Delta
			}
			got = append(got, strings.TrimSpace(s))
			last = ev
		}
	}
	want := []string{
		"response.start",
		"item.start reasoning", "reasoning.delta Think", "reasoning.delta ing.", "item.end",
		"item.start tool_call call_a",
		"item.start tool_call call_b", `tool_input.delta {"y"`,
		`tool_input.delta {"x":1}`, "tool_input.delta :2}",
		"item.end", "item.end",
		"finish",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if last.Finish.Reason != llm.FinishToolCalls || last.Finish.RawReason != "tool_calls" {
		t.Errorf("finish = %+v", last.Finish)
	}
	u := last.Usage
	if *u.InputTokens != 20 || *u.OutputTokens != 9 || *u.CachedInputTokens != 4 || *u.ReasoningTokens != 3 {
		t.Errorf("usage = %+v", u)
	}
}

func TestStreamDecoderEndsEarly(t *testing.T) {
	d := NewStreamDecoder()
	if _, err := d.Decode([]byte(`{"id":"x","model":"m","choices":[{"index":0,"delta":{"content":"Hi"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Close(); err == nil {
		t.Fatal("a stream that ended without a finish reason or [DONE] was accepted")
	}
}

func TestStreamWriter(t *testing.T) {
	var b bytes.Buffer
	w := NewStreamWriter(&b, true)
	for _, ev := range []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Created: 9}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "hmm"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_a", Name: "f"}},
		{Type: llm.EventToolInputDelta, Index: 1, Delta: `{"x":1}`},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}, Usage: &llm.Usage{InputTokens: llm.Int64(5), OutputTokens: llm.Int64(2)}},
	} {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	h := `"id":"r1","object":"chat.completion.chunk","created":9,"model":"m"`
	want := "" +
		`data: {` + h + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
		`data: {` + h + `,"choices":[{"index":0,"delta":{"reasoning_content":"hmm"},"finish_reason":null}]}` + "\n\n" +
		`data: {` + h + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {` + h + `,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {` + h + `,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {` + h + `,"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n" +
		"data: [DONE]\n\n"
	if b.String() != want {
		t.Errorf("stream:\n%s\nwant:\n%s", b.String(), want)
	}
}
