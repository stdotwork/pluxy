package anthropicmessages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A multi-turn request with extended thinking, prompt caching and tool use, as Claude Code sends
// one.
const thinkingToolTurn = `{"model":"m","max_tokens":1024,"metadata":{"user_id":"u"},"top_k":5,
"thinking":{"type":"enabled","budget_tokens":4096},
"system":[{"type":"text","text":"Be brief."},{"type":"text","text":"Use tools.","cache_control":{"type":"ephemeral","ttl":"1h"}}],
"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
 {"type":"web_search_20250305","name":"web_search"}],
"messages":[
 {"role":"user","content":"Weather in Paris and Rome?"},
 {"role":"assistant","content":[
  {"type":"thinking","thinking":"Need both.","signature":"sig-1"},
  {"type":"redacted_thinking","data":"opaque"},
  {"type":"text","text":"Checking."},
  {"type":"tool_use","id":"toolu_A","name":"get_weather","input":{"city":"Paris"}},
  {"type":"tool_use","id":"toolu_B","name":"get_weather","input":{"city":"Rome"}}]},
 {"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_B","content":"18C"},
  {"type":"tool_result","tool_use_id":"toolu_A","content":[{"type":"text","text":"21C"}],"is_error":false},
  {"type":"text","text":"Which is warmer?","cache_control":{"type":"ephemeral"}}]}
]}`

func lossList(losses []llm.Loss) []string {
	var out []string
	for _, l := range losses {
		out = append(out, l.Kind+" "+l.Path)
	}
	slices.Sort(out)
	return out
}

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

// A Messages request translated to Chat Completions keeps the order of thinking, text, tool calls
// and tool results, and reports by path everything Chat Completions has no place for: signatures,
// redacted thinking, cache_control, server tools and Anthropic-only options.
func TestTranslationToChatReportsLosses(t *testing.T) {
	req, err := ParseRequest([]byte(thinkingToolTurn))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"approximated messages[1].content[0]",          // thinking text becomes reasoning_content
		"dropped messages[1].content[0].signature",     // only Anthropic can check it
		"dropped messages[1].content[1]",               // redacted thinking
		"dropped messages[2].content[1].is_error",      // no error flag on a Chat tool message
		"dropped messages[2].content[2].cache_control", // no cache breakpoints in Chat
		"dropped metadata",
		"dropped system[1].cache_control",
		"dropped thinking", // the budget; the effort is carried
		"dropped tools[0].cache_control",
		"dropped tools[1]", // a server tool
		"dropped top_k",
	}
	slices.Sort(want)
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses:\n got %q\nwant %q", got, want)
	}
	sameJSON(t, "body", body, `{"model":"m","stream":false,"max_tokens":1024,"reasoning_effort":"medium",
	 "tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object"}}}],
	 "messages":[
	  {"role":"system","content":[{"type":"text","text":"Be brief."},{"type":"text","text":"Use tools."}]},
	  {"role":"user","content":"Weather in Paris and Rome?"},
	  {"role":"assistant","content":[{"type":"text","text":"Checking."}],"reasoning_content":"Need both.","tool_calls":[
	   {"id":"toolu_A","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
	   {"id":"toolu_B","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"}}]},
	  {"role":"tool","content":"18C","tool_call_id":"toolu_B"},
	  {"role":"tool","content":"21C","tool_call_id":"toolu_A"},
	  {"role":"user","content":[{"type":"text","text":"Which is warmer?"}]}]}`)
}

// The same request translated to Responses keeps every item in place; thinking from another
// provider is dropped and reported, since a Responses upstream cannot replay it.
func TestTranslationToResponsesReportsLosses(t *testing.T) {
	req, err := ParseRequest([]byte(thinkingToolTurn))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openairesponses.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dropped messages[1].content[0]", // thinking
		"dropped messages[1].content[1]", // redacted thinking
		"dropped messages[2].content[1].is_error",
		"dropped messages[2].content[2].cache_control",
		"dropped metadata",
		"dropped system[1].cache_control",
		"dropped thinking",
		"dropped tools[0].cache_control",
		"dropped tools[1]",
		"dropped top_k",
	}
	slices.Sort(want)
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses:\n got %q\nwant %q", got, want)
	}
	sameJSON(t, "body", body, `{"model":"m","stream":false,"max_output_tokens":1024,"reasoning":{"effort":"medium"},
	 "tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object"}}],
	 "input":[
	  {"type":"message","role":"system","content":[{"type":"input_text","text":"Be brief."},{"type":"input_text","text":"Use tools."}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"Weather in Paris and Rome?"}]},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking."}]},
	  {"type":"function_call","call_id":"toolu_A","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
	  {"type":"function_call","call_id":"toolu_B","name":"get_weather","arguments":"{\"city\":\"Rome\"}"},
	  {"type":"function_call_output","call_id":"toolu_B","output":"18C"},
	  {"type":"function_call_output","call_id":"toolu_A","output":"21C"},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"Which is warmer?"}]}]}`)
}

// Thinking budgets map to the reasoning effort today's core picks for them.
func TestThinkingEffort(t *testing.T) {
	for _, c := range []struct{ thinking, config, want string }{
		{`{"type":"enabled","budget_tokens":0}`, ``, "none"},
		{`{"type":"enabled","budget_tokens":512}`, ``, "minimal"},
		{`{"type":"enabled","budget_tokens":1024}`, ``, "low"},
		{`{"type":"enabled","budget_tokens":8192}`, ``, "medium"},
		{`{"type":"enabled","budget_tokens":24576}`, ``, "high"},
		{`{"type":"enabled","budget_tokens":24577}`, ``, "xhigh"},
		{`{"type":"enabled"}`, `{"effort":"High"}`, "high"},
		{`{"type":"adaptive"}`, ``, "xhigh"},
		{`{"type":"adaptive"}`, `{"effort":"low"}`, "low"},
		{`{"type":"disabled"}`, ``, "none"},
	} {
		body := `{"model":"m","messages":[],"thinking":` + c.thinking
		if c.config != "" {
			body += `,"output_config":` + c.config
		}
		req, err := ParseRequest([]byte(body + "}"))
		if err != nil {
			t.Fatal(err)
		}
		if req.Options.ReasoningEffort != c.want {
			t.Errorf("thinking %s output_config %s: effort %q, want %q", c.thinking, c.config, req.Options.ReasoningEffort, c.want)
		}
	}
}

func TestParseRequestRejects(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"messages":[]}`,
		`{"model":"m","messages":[{"role":"system","content":"x"}]}`,
		`{"model":"m","messages":[{"role":"user","content":7}]}`,
		`{"model":"m","messages":[{"role":"user","content":["x"]}]}`,
		`{"model":"m","system":7,"messages":[]}`,
	} {
		_, err := ParseRequest([]byte(body))
		var e *llm.Error
		if !asError(err, &e) || e.Code != llm.CodeInvalidRequest {
			t.Errorf("%s: got %v, want invalid_request", body, err)
		}
	}
}

func asError(err error, e **llm.Error) bool {
	le, ok := err.(*llm.Error)
	*e = le
	return ok
}

// An upstream Messages stream: every upstream event reaches the model events once, in Raw, events
// the model has no place for become native events, the stream follows the protocol's ordering
// rules, and usage follows Anthropic's rule that input_tokens leaves out cache reads and writes.
func TestStreamDecoderKeepsUpstreamEvents(t *testing.T) {
	stream := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"stop_reason":null,"usage":{"input_tokens":12,"cache_read_input_tokens":100,"cache_creation_input_tokens":5,"output_tokens":1}}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Hmm."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`,
	}
	d := NewStreamDecoder()
	var events []llm.Event
	for _, s := range stream {
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		events = append(events, evs...)
	}
	if evs, err := d.Close(); err != nil || len(evs) != 0 {
		t.Fatalf("Close: %v %v", evs, err)
	}
	var seq llm.Sequence
	var raws []string
	var types []string
	for _, ev := range events {
		if err := seq.Check(ev); err != nil {
			t.Fatalf("sequence: %v", err)
		}
		types = append(types, ev.Type)
		if len(ev.Raw) > 0 {
			raws = append(raws, string(ev.Raw))
		}
	}
	if !reflect.DeepEqual(raws, stream) {
		t.Errorf("raws:\n got %q\nwant %q", raws, stream)
	}
	wantTypes := []string{"response.start", "native", "item.start", "reasoning.delta", "native", "item.end", "native", "native",
		"item.start", "tool_input.delta", "tool_input.delta", "item.end", "native", "finish"}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Errorf("types:\n got %q\nwant %q", types, wantTypes)
	}
	last := events[len(events)-1]
	if last.Finish.Reason != llm.FinishToolCalls || last.Finish.RawReason != "tool_use" {
		t.Errorf("finish %+v", last.Finish)
	}
	u := last.Usage
	if u == nil || *u.InputTokens != 117 || *u.CachedInputTokens != 100 || *u.CacheWriteInputTokens != 5 || *u.OutputTokens != 30 {
		t.Errorf("usage %+v, want input 117 (12+100+5), cached 100, cache write 5, output 30", u)
	}
}

func TestStreamDecoderFailures(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg_1","model":"up","usage":{"input_tokens":1}}}`
	t.Run("error event", func(t *testing.T) {
		d := NewStreamDecoder()
		_, _ = d.Decode([]byte(start))
		raw := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
		_, err := d.Decode([]byte(raw))
		e, ok := err.(*llm.Error)
		if !ok || e.Message != "Overloaded" || e.ProviderCode != "overloaded_error" || string(e.Raw) != raw {
			t.Errorf("got %#v", err)
		}
		if e, ok := err.(*llm.Error); !ok || e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 1 {
			t.Errorf("error event: usage %#v, want the 1 input token message_start reported", err)
		}
	})
	t.Run("ends before message_stop", func(t *testing.T) {
		d := NewStreamDecoder()
		_, _ = d.Decode([]byte(start))
		_, _ = d.Decode([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		_, err := d.Close()
		if err == nil {
			t.Fatal("Close after a cut-short stream: no error")
		}
		if e, ok := err.(*llm.Error); !ok || e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 1 {
			t.Errorf("cut-short stream: usage %#v, want the 1 input token message_start reported", err)
		}
		if u := d.Usage(); u == nil || u.InputTokens == nil || *u.InputTokens != 1 {
			t.Errorf("Usage() = %#v, want the 1 input token message_start reported", u)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		if _, err := NewStreamDecoder().Decode([]byte(`{nope`)); err == nil {
			t.Error("no error")
		}
	})
}

// A reply from a Messages upstream reaches a Messages client exactly as the upstream sent it.
func TestNativeStreamPassesThrough(t *testing.T) {
	stream := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"usage":{"input_tokens":3,"output_tokens":1},"x_vendor":1}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"He"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"llo"}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"pause_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	var want bytes.Buffer
	d := NewStreamDecoder()
	var out bytes.Buffer
	w := NewStreamWriter(&out)
	for _, s := range stream {
		var typ struct{ Type string }
		_ = json.Unmarshal([]byte(s), &typ)
		want.WriteString("event: " + typ.Type + "\ndata: " + s + "\n\n")
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out.String() != want.String() {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want.String())
	}

	// A mid-stream upstream error event passes through too.
	d, out = NewStreamDecoder(), bytes.Buffer{}
	w = NewStreamWriter(&out)
	evs, _ := d.Decode([]byte(stream[0]))
	for _, ev := range evs {
		_ = w.Write(ev)
	}
	errRaw := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	_, err := d.Decode([]byte(errRaw))
	_ = w.Write(llm.Event{Type: llm.EventError, Error: err.(*llm.Error)})
	if !strings.HasSuffix(out.String(), "event: error\ndata: "+errRaw+"\n\n") {
		t.Errorf("error event not passed through:\n%s", out.String())
	}
}

// A reply from a Chat upstream is rebuilt as a Messages stream, with Messages usage: input_tokens
// without the cached tokens, which go in cache_read_input_tokens.
func TestStreamWriterRebuildsEvents(t *testing.T) {
	var out bytes.Buffer
	w := NewStreamWriter(&out)
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "chatcmpl-1", Model: "up", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "Think."},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 1, Delta: "Hi"},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventItemStart, Index: 2, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_1", Name: "f"}},
		{Type: llm.EventToolInputDelta, Index: 2, Delta: `{"a":1}`},
		{Type: llm.EventItemEnd, Index: 2},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls, RawReason: "tool_calls"},
			Usage: &llm.Usage{InputTokens: llm.Int64(120), CachedInputTokens: llm.Int64(100), OutputTokens: llm.Int64(6)}},
	}
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		`message_start {"type":"message_start","message":{"id":"chatcmpl-1","type":"message","role":"assistant","model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`,
		`content_block_start {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`content_block_delta {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Think."}}`,
		`content_block_stop {"type":"content_block_stop","index":0}`,
		`content_block_start {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`content_block_delta {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}`,
		`content_block_stop {"type":"content_block_stop","index":1}`,
		`content_block_start {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_1","name":"f","input":{}}}`,
		`content_block_delta {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
		`content_block_stop {"type":"content_block_stop","index":2}`,
		`message_delta {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":20,"output_tokens":6,"cache_read_input_tokens":100}}`,
		`message_stop {"type":"message_stop"}`,
	}
	chunks := strings.Split(strings.TrimSuffix(out.String(), "\n\n"), "\n\n")
	if len(chunks) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(chunks), len(want), out.String())
	}
	for i, c := range chunks {
		lines := strings.SplitN(c, "\n", 2)
		typ, data := strings.TrimPrefix(lines[0], "event: "), strings.TrimPrefix(lines[1], "data: ")
		wantTyp, wantData, _ := strings.Cut(want[i], " ")
		if typ != wantTyp {
			t.Errorf("event %d: type %q, want %q", i, typ, wantTyp)
		}
		sameJSON(t, "event "+wantTyp, []byte(data), wantData)
	}
}

// Error replies use Anthropic's error envelope and type names.
func TestErrorResponse(t *testing.T) {
	for _, c := range []struct {
		e      *llm.Error
		status int
		want   string
	}{
		{&llm.Error{Code: llm.CodeRateLimit, Message: "slow down", Status: 429}, 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`},
		{&llm.Error{Code: llm.CodeInvalidRequest, Message: "bad"}, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`},
		{&llm.Error{Code: llm.CodeUpstream, Message: "overloaded", Status: 529}, 529, `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`},
		{&llm.Error{Code: llm.CodeUpstream, Message: "broke"}, 502, `{"type":"error","error":{"type":"api_error","message":"broke"}}`},
		// An Anthropic error event at the start of a stream keeps its type and gets its status.
		{&llm.Error{Code: llm.CodeUpstream, Message: "Overloaded", ProviderCode: "overloaded_error"}, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	} {
		status, body := ErrorResponse(c.e)
		if status != c.status {
			t.Errorf("%v: status %d, want %d", c.e, status, c.status)
		}
		sameJSON(t, c.e.Error(), body, c.want)
	}
}

// writeAll writes events through a stream writer and returns the Messages events as
// "type data" lines.
func writeAll(t *testing.T, events []llm.Event) []string {
	t.Helper()
	var out bytes.Buffer
	w := NewStreamWriter(&out)
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	var lines []string
	for _, c := range strings.Split(strings.TrimSuffix(out.String(), "\n\n"), "\n\n") {
		parts := strings.SplitN(c, "\n", 2)
		lines = append(lines, strings.TrimPrefix(parts[0], "event: ")+" "+strings.TrimPrefix(parts[1], "data: "))
	}
	return lines
}

// Content blocks in a Messages stream do not overlap: parallel tool calls a Chat upstream
// interleaves are written one block after the other, the later one held until the first ends.
func TestStreamWriterSerialisesParallelBlocks(t *testing.T) {
	a := &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_a", Name: "f"}
	b := &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_b", Name: "g"}
	got := writeAll(t, []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Model: "up", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: a},
		{Type: llm.EventToolInputDelta, Index: 0, Delta: `{"x":`},
		{Type: llm.EventItemStart, Index: 1, Item: b},
		{Type: llm.EventToolInputDelta, Index: 1, Delta: `{"y":`},
		{Type: llm.EventToolInputDelta, Index: 0, Delta: `1}`},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventToolInputDelta, Index: 1, Delta: `2}`},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}},
	})
	want := []string{
		`content_block_start {"content_block":{"id":"call_a","input":{},"name":"f","type":"tool_use"},"index":0,"type":"content_block_start"}`,
		`content_block_delta {"delta":{"partial_json":"{\"x\":","type":"input_json_delta"},"index":0,"type":"content_block_delta"}`,
		`content_block_delta {"delta":{"partial_json":"1}","type":"input_json_delta"},"index":0,"type":"content_block_delta"}`,
		`content_block_stop {"index":0,"type":"content_block_stop"}`,
		`content_block_start {"content_block":{"id":"call_b","input":{},"name":"g","type":"tool_use"},"index":1,"type":"content_block_start"}`,
		`content_block_delta {"delta":{"partial_json":"{\"y\":","type":"input_json_delta"},"index":1,"type":"content_block_delta"}`,
		`content_block_delta {"delta":{"partial_json":"2}","type":"input_json_delta"},"index":1,"type":"content_block_delta"}`,
		`content_block_stop {"index":1,"type":"content_block_stop"}`,
	}
	if len(got) < 2+len(want) || !reflect.DeepEqual(got[1:1+len(want)], want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}

// A non-streamed Message from a Messages upstream, written to a streaming client, comes out as
// stream events that put the same Message back together: signatures, redacted thinking, the
// stop sequence and every usage field survive.
func TestNonStreamedMessageAsStream(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"up","x_vendor":1,"content":[
	 {"type":"thinking","thinking":"Hmm.","signature":"sig-1"},
	 {"type":"redacted_thinking","data":"opaque"},
	 {"type":"text","text":"Hi","citations":[{"type":"char_location","cited_text":"x"}]},
	 {"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}],
	 "stop_reason":"stop_sequence","stop_sequence":"END",
	 "usage":{"input_tokens":3,"cache_read_input_tokens":10,"output_tokens":7,"service_tier":"standard"}}`
	events, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var seq llm.Sequence
	for _, ev := range events {
		if err := seq.Check(ev); err != nil {
			t.Fatalf("sequence: %v", err)
		}
	}
	lines := writeAll(t, events)
	var acc accumulator
	var types []string
	for _, l := range lines {
		typ, data, _ := strings.Cut(l, " ")
		types = append(types, typ)
		acc.add(json.RawMessage(data))
	}
	wantTypes := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop", "content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Errorf("event types:\n got %q\nwant %q", types, wantTypes)
	}
	got, err := acc.result()
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "message put back together", got, body)
}

// A streamed reply to a non-streaming Messages client is put together into the Message the
// stream describes, keeping what the model has no place for.
func TestAggregatorPutsStreamTogether(t *testing.T) {
	stream := []string{
		`{"type":"ping"}`,
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_read_input_tokens":100,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Hmm."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"END"},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`,
	}
	d := NewStreamDecoder()
	a := NewAggregator()
	var last llm.Event
	for _, s := range stream {
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			a.Add(ev)
			last = ev
		}
	}
	got, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "message", got, `{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[
	 {"type":"thinking","thinking":"Hmm.","signature":"sig"},
	 {"type":"redacted_thinking","data":"opaque"},
	 {"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}],
	 "stop_reason":"stop_sequence","stop_sequence":"END","usage":{"input_tokens":12,"cache_read_input_tokens":100,"output_tokens":30}}`)
	// The ping before message_start does not lose message_start's usage.
	if u := last.Usage; u == nil || u.InputTokens == nil || *u.InputTokens != 112 {
		t.Errorf("finish usage %+v, want input 112 (12+100)", u)
	}
}

// Upstream event types the decoder does not know reach a Messages client unchanged.
func TestNativeStreamPassesUnknownEvents(t *testing.T) {
	stream := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"usage":{"input_tokens":3}}}`,
		`{"type":"future_event","data":"keep-me"}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	}
	d := NewStreamDecoder()
	var events []llm.Event
	for _, s := range stream {
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	got := writeAll(t, events)
	if len(got) != len(stream) || got[1] != "future_event "+stream[1] {
		t.Errorf("got %q", got)
	}
}

// Assistant text after a tool call in the same turn stays in the Chat message that holds the
// call, so the tool result still follows that message.
func TestChatKeepsAssistantTurnWhole(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","messages":[
	 {"role":"user","content":"Weather?"},
	 {"role":"assistant","vendor":true,"content":[{"type":"tool_use","id":"toolu_A","name":"f","input":{}},{"type":"text","text":"Checking."}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"21C"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "body", body, `{"model":"m","stream":false,"messages":[
	 {"role":"user","content":"Weather?"},
	 {"role":"assistant","content":[{"type":"text","text":"Checking."}],"tool_calls":[{"id":"toolu_A","type":"function","function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","content":"21C","tool_call_id":"toolu_A"}]}`)
	want := []string{"approximated messages[1]", "dropped messages[1].vendor"}
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses %q, want %q", got, want)
	}
}

// A message whose blocks are all tool calls keeps its own fields, so a translation reports them.
func TestMessageFieldsWithoutContentItem(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","messages":[
	 {"role":"assistant","vendor":true,"content":[{"type":"tool_use","id":"toolu_A","name":"f","input":{}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := lossList(losses); !reflect.DeepEqual(got, []string{"dropped messages[0].vendor"}) {
		t.Errorf("losses %q", got)
	}
}

// What a reply from another format has that a Message cannot hold is reported: usage details,
// arguments that are not a JSON object.
func TestAggregatorReportsReplyLosses(t *testing.T) {
	a := NewAggregator()
	for _, ev := range []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Model: "up", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_1", Name: "f"}},
		{Type: llm.EventToolInputDelta, Index: 0, Delta: `{"x":`},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}, Usage: &llm.Usage{InputTokens: llm.Int64(5), OutputTokens: llm.Int64(9),
			Raw: json.RawMessage(`{"prompt_tokens":5,"completion_tokens":9,"completion_tokens_details":{"reasoning_tokens":4,"audio_tokens":0},"prompt_tokens_details":{"cached_tokens":0}}`)}},
	} {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"input":{}`) {
		t.Errorf("body %s", body)
	}
	want := []string{"dropped content[0].input", "dropped usage.completion_tokens_details.reasoning_tokens"}
	if got := lossList(a.Losses()); !reflect.DeepEqual(got, want) {
		t.Errorf("losses %q, want %q", got, want)
	}
}

// Assistant text on both sides of a tool call stays in the one Chat message holding the call.
func TestChatKeepsTextAroundToolCall(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","messages":[
	 {"role":"user","content":"Weather?"},
	 {"role":"assistant","content":[{"type":"text","text":"Before"},{"type":"tool_use","id":"toolu_A","name":"f","input":{}},{"type":"text","text":"After"}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"21C"}],"vendor":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "body", body, `{"model":"m","stream":false,"messages":[
	 {"role":"user","content":"Weather?"},
	 {"role":"assistant","content":[{"type":"text","text":"Before"},{"type":"text","text":"After"}],"tool_calls":[{"id":"toolu_A","type":"function","function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","content":"21C","tool_call_id":"toolu_A"}]}`)
	want := []string{"approximated messages[1]", "dropped messages[2].vendor"}
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses %q, want %q", got, want)
	}
}

// Stream events with JSON whitespace before them are put together like any other.
func TestAggregatorTakesEventsWithLeadingSpace(t *testing.T) {
	d := NewStreamDecoder()
	a := NewAggregator()
	for _, s := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"usage":{"input_tokens":1}}}`,
		` {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"\t" + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	} {
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			a.Add(ev)
		}
	}
	got, _ := a.Response()
	if !strings.Contains(string(got), `"text":"hello"`) {
		t.Errorf("got %s", got)
	}
}

// Stop fields of a non-streamed Message come in message_delta when it is written as a stream, as
// Anthropic's SDKs read them from there.
func TestNonStreamedStopFieldsInMessageDelta(t *testing.T) {
	events, err := DecodeResponse([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],
	 "stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber"},"usage":{"input_tokens":1,"output_tokens":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := writeAll(t, events)
	var start, delta string
	for _, l := range lines {
		typ, data, _ := strings.Cut(l, " ")
		switch typ {
		case "message_start":
			start = data
		case "message_delta":
			delta = data
		}
	}
	if !strings.Contains(start, `"stop_details":null`) || !strings.Contains(delta, `"stop_details":{"type":"refusal","category":"cyber"}`) {
		t.Errorf("message_start %s\nmessage_delta %s", start, delta)
	}
}

// A Responses stream that opens with its terminal event still has its encrypted reasoning
// reported as dropped.
func TestResponsesTerminalFirstReportsLosses(t *testing.T) {
	d := openairesponses.NewStreamDecoder()
	evs, err := d.Decode([]byte(`{"type":"response.completed","response":{"id":"r1","status":"completed","model":"up","output":[
	 {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAA"},
	 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[{"type":"url_citation"}]}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	want := []string{"dropped output[0].encrypted_content", "dropped output[1].content[0].annotations"}
	if got := lossList(a.Losses()); !reflect.DeepEqual(got, want) {
		t.Errorf("losses %q, want %q", got, want)
	}
}

// A Gemini GenerateContent request, read by its parser and encoded to Messages: the system
// instruction becomes the system prompt, each turn's calls and results pair up by id, and Gemini's
// thought signature and reasoning effort have no place in Messages and are reported.
func TestGeminiRequestEncodesToMessages(t *testing.T) {
	const body = `{"systemInstruction":{"parts":[{"text":"Be brief."}]},
	"contents":[
	 {"role":"user","parts":[{"text":"Weather?"}]},
	 {"role":"model","parts":[
	  {"text":"Checking."},
	  {"functionCall":{"id":"c1","name":"w","args":{"city":"P"}},"thoughtSignature":"sig"},
	  {"functionCall":{"id":"c2","name":"w","args":{"city":"B"}}}]},
	 {"role":"user","parts":[
	  {"functionResponse":{"id":"c1","name":"w","response":{"t":1}}},
	  {"functionResponse":{"id":"c2","name":"w","response":{"t":2}}}]}],
	"tools":[{"functionDeclarations":[{"name":"w","parametersJsonSchema":{"type":"object"}}]}],
	"generationConfig":{"temperature":0.5,"stopSequences":["X"],"thinkingConfig":{"thinkingBudget":2048}}}`
	req, err := geminigenerate.ParseRequest("m", false, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "body", out, `{"model":"m","stream":false,"max_tokens":32000,"temperature":0.5,"stop_sequences":["X"],
	 "system":[{"type":"text","text":"Be brief."}],
	 "tools":[{"name":"w","input_schema":{"type":"object"}}],
	 "messages":[
	  {"role":"user","content":[{"type":"text","text":"Weather?"}]},
	  {"role":"assistant","content":[
	   {"type":"text","text":"Checking."},
	   {"type":"tool_use","id":"c1","name":"w","input":{"city":"P"}},
	   {"type":"tool_use","id":"c2","name":"w","input":{"city":"B"}}]},
	  {"role":"user","content":[
	   {"type":"tool_result","tool_use_id":"c1","content":"{\"t\":1}"},
	   {"type":"tool_result","tool_use_id":"c2","content":"{\"t\":2}"}]}]}`)
	got := lossList(losses)
	for _, want := range []string{"dropped contents[1].parts[1].thoughtSignature", "dropped reasoning_effort"} {
		if !slices.Contains(got, want) {
			t.Errorf("losses %q do not include %q", got, want)
		}
	}
}

// Encrypted reasoning from a Responses upstream reaches a Messages client as its thinking block's
// signature, whether the upstream streamed or sent one JSON Response, and whether the client
// streams or not. Because the signature carries it, it is not reported as dropped.
func TestResponsesReasoningCarriedAsSignature(t *testing.T) {
	sig := base64.URLEncoding.EncodeToString(append([]byte{0x80}, make([]byte, 8+16+16+32)...))
	reasoning := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Think."}],"encrypted_content":"` + sig + `"}`
	message := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}`
	response := `{"id":"r1","object":"response","status":"completed","model":"up","output":[` + reasoning + `,` + message + `]}`

	streamed := func(t *testing.T) []llm.Event {
		d := openairesponses.NewStreamDecoder()
		var evs []llm.Event
		for _, data := range []string{
			`{"type":"response.created","response":{"id":"r1","status":"in_progress","model":"up","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Think."}`,
			`{"type":"response.output_item.done","output_index":0,"item":` + reasoning + `}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"Hi"}`,
			`{"type":"response.output_item.done","output_index":1,"item":` + message + `}`,
			`{"type":"response.completed","response":` + response + `}`,
		} {
			out, err := d.Decode([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, out...)
		}
		return evs
	}
	whole := func(t *testing.T) []llm.Event {
		evs, err := openairesponses.DecodeResponse([]byte(response))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	for name, upstream := range map[string]func(*testing.T) []llm.Event{"streamed": streamed, "json": whole} {
		t.Run(name+"/stream", func(t *testing.T) {
			var out bytes.Buffer
			w := NewStreamWriter(&out)
			for _, ev := range upstream(t) {
				if err := w.Write(ev); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(out.String(), `{"signature":"`+sig+`","type":"signature_delta"}`) {
				t.Errorf("no signature_delta carrying the encrypted reasoning:\n%s", out.String())
			}
			if got := lossList(w.Losses()); slices.Contains(got, "dropped output[0].encrypted_content") {
				t.Errorf("carried reasoning reported as dropped: %q", got)
			}
		})
		t.Run(name+"/aggregate", func(t *testing.T) {
			a := NewAggregator()
			for _, ev := range upstream(t) {
				a.Add(ev)
			}
			body, err := a.Response()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"signature":"`+sig+`"`) {
				t.Errorf("no signature carrying the encrypted reasoning: %s", body)
			}
			if got := lossList(a.Losses()); slices.Contains(got, "dropped output[0].encrypted_content") {
				t.Errorf("carried reasoning reported as dropped: %q", got)
			}
		})
	}
}

// A Claude thinking signature survives a Responses client's round trip: the reply's reasoning
// item carries it as encrypted_content, streamed or not, and when the client replays that item
// it goes back to Messages as the thinking block, in the assistant turn of the tool call it
// preceded.
func TestThinkingSignatureRoundTripsThroughResponses(t *testing.T) {
	const sig = "EqoBCkgIARABGAIiQRoundTripSig0"
	stream := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Look it up."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + sig + `"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	}
	dec := NewStreamDecoder()
	var events []llm.Event
	for _, data := range stream {
		evs, err := dec.Decode([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	req, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	w := openairesponses.NewStreamWriter(&out, req)
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), `"encrypted_content":"`+sig+`"`) {
		t.Errorf("streamed Responses reply has no encrypted_content %q:\n%s", sig, out.String())
	}

	body := `{"type":"message","id":"msg_1","role":"assistant","model":"m","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":5},` +
		`"content":[{"type":"thinking","thinking":"Look it up.","signature":"` + sig + `"}]}`
	events, err = DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	agg := openairesponses.NewAggregator(req)
	for _, ev := range events {
		agg.Add(ev)
	}
	resp, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), `"encrypted_content":"`+sig+`"`) {
		t.Errorf("Responses reply has no encrypted_content %q: %s", sig, resp)
	}

	replay, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":[
	 {"type":"message","role":"user","content":"Weather?"},
	 {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Look it up."}],"encrypted_content":"` + sig + `"},
	 {"type":"function_call","call_id":"c1","name":"w","arguments":"{}"},
	 {"type":"function_call_output","call_id":"c1","output":"sunny"},
	 {"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAAABopenai"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, losses, err := EncodeRequest(replay)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "body", encoded, `{"model":"m","stream":false,"max_tokens":32000,"messages":[
	 {"role":"user","content":[{"type":"text","text":"Weather?"}]},
	 {"role":"assistant","content":[
	  {"type":"thinking","thinking":"Look it up.","signature":"`+sig+`"},
	  {"type":"tool_use","id":"c1","name":"w","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"sunny"}]}]}`)
	if got := lossList(losses); !slices.Contains(got, "dropped input[4]") {
		t.Errorf("losses %q: the OpenAI reasoning item is not reported dropped", got)
	}
}

// Signature deltas are collected for the item's end, up to a limit: a stream that keeps sending
// them fails instead of growing without bound.
func TestStreamDecoderBoundsSignature(t *testing.T) {
	dec := NewStreamDecoder()
	for _, data := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	} {
		if _, err := dec.Decode([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	piece := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + strings.Repeat("E", 64<<10) + `"}}`
	for range MaxSignature/(64<<10) + 1 {
		if _, err := dec.Decode([]byte(piece)); err != nil {
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Retryable {
				t.Fatalf("err = %v, want a non-retryable upstream error", err)
			}
			return
		}
	}
	t.Fatal("the decoder took signature deltas beyond MaxSignature")
}

// What the reply reports as lost counts toward its size, so core's limit on a non-streamed reply
// bounds it: each reasoning item of a Responses upstream adds a loss of its own.
func TestAggregatorSizeCountsLosses(t *testing.T) {
	const n = 1000
	items := make([]string, n)
	want := 0
	for i := range items {
		items[i] = `{"type":"reasoning","encrypted_content":"x"}`
		want += len(fmt.Sprintf("output[%d].encrypted_content", i))
	}
	evs, err := openairesponses.NewStreamDecoder().Decode([]byte(`{"type":"response.completed","response":{"id":"r1","status":"completed","model":"up","output":[` + strings.Join(items, ",") + `]}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	if len(a.Losses()) != n {
		t.Fatalf("%d losses, want %d", len(a.Losses()), n)
	}
	if got := a.Size(); got < want {
		t.Errorf("Size() = %d, want at least the %d bytes of the paths it reports lost", got, want)
	}
}
