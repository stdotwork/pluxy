package openaichat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
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
	out, _, err := EncodeRequest(req)
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

// Fields the model does not interpret survive a Chat request read into items and written back
// as Chat: a message's name, an assistant's reasoning_content, vendor fields on the request, a
// message, a part, a tool call and a tool, a tool's strict flag and a tool type the model does
// not know. (The encoder always writes stream, so the input states it.)
func TestParseThenEncodeKeepsUnmodelledFields(t *testing.T) {
	in := `{"model":"m","stream":false,"metadata":{"team":"a"},"x_vendor":{"k":1},
	"tools":[{"type":"function","x_tool":true,"function":{"name":"f","parameters":{"type":"object"},"strict":true,"x_fn":2}},
	 {"type":"custom","custom":{"name":"patch"}}],
	"messages":[
	 {"role":"user","name":"alice","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],"x_msg":"u"},
	 {"role":"assistant","content":"Calling.","reasoning_content":"Need f.","tool_calls":[
	   {"id":"call_1","type":"function","x_call":3,"function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","tool_call_id":"call_1","content":"done","x_tool_msg":4}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("same-format encode reported losses: %+v", losses)
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("encoded:\n got %s\nwant %s", out, compactJSON(t, in))
	}
}

// More shapes survive a Chat round trip: an empty text part keeps its text field, fields inside
// stream_options, response_format and a custom tool call are kept, and an assistant message that
// only carries tool calls keeps its own fields.
func TestParseThenEncodeKeepsNestedFields(t *testing.T) {
	in := `{"model":"m","stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false},
	"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"},"x_schema":1}},
	"messages":[
	 {"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]},
	 {"role":"assistant","name":"bot","tool_calls":[{"id":"c1","type":"custom","custom":{"name":"patch","input":"*** Begin"}}]},
	 {"role":"tool","tool_call_id":"c1","content":"ok"}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("same-format encode reported losses: %+v", losses)
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("encoded:\n got %s\nwant %s", out, compactJSON(t, in))
	}
}

// An assistant message with empty content keeps content "" (Chat Completions needs content when a
// message has no tool calls); one that only carries tool calls has none.
func TestEncodeEmptyAssistantContent(t *testing.T) {
	req := llm.Request{Model: "m", Items: []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Origin: &llm.Origin{Format: llm.FormatOpenAIResponses}},
		{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Origin: &llm.Origin{Format: llm.FormatOpenAIResponses}},
		{Type: llm.ItemTypeToolCall, CallID: "c", Name: "f", Arguments: "{}"},
	}}
	out, _, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","messages":[{"role":"assistant","content":""},
	 {"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}],"stream":false}`
	if got, w := string(out), compactJSON(t, want); got != w {
		t.Errorf("encoded:\n got %s\nwant %s", got, w)
	}
}

func TestParseRequestRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":      `{`,
		"no model":      `{"messages":[{"role":"user","content":"hi"}]}`,
		"no messages":   `{"model":"m","messages":[]}`,
		"unknown role":  `{"model":"m","messages":[{"role":"wizard","content":"hi"}]}`,
		"non-text tool": `{"model":"m","messages":[{"role":"tool","tool_call_id":"c","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		"bad stop":      `{"model":"m","stop":7,"messages":[{"role":"user","content":"hi"}]}`,
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

	// [DONE] does not finish a reply that never carried a finish reason: the upstream may have
	// cut it short.
	d = NewStreamDecoder()
	if _, err := d.Decode([]byte(`{"id":"x","model":"m","choices":[{"index":0,"delta":{"content":"Hi"}}]}`)); err != nil {
		t.Fatal(err)
	}
	evs, err := d.Decode([]byte(`[DONE]`))
	var le *llm.Error
	if !errors.As(err, &le) || le.Code != llm.CodeUpstream {
		t.Fatalf("[DONE] without a finish reason gave events %+v, err %v; want an upstream error", evs, err)
	}
}

// A reply with several choices is finished only when every choice it returned carried a finish
// reason: an unfinished choice after the first may have been cut short as much as the first.
func TestStreamDecoderWaitsForEveryChoice(t *testing.T) {
	const (
		both      = `{"id":"x","model":"m","choices":[{"index":0,"delta":{"content":"A"}},{"index":1,"delta":{"content":"B"}}]}`
		firstDone = `{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
		otherDone = `{"id":"x","model":"m","choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`
	)
	for _, c := range []struct {
		name   string
		chunks []string
		end    string // "[DONE]", or "" for the stream closing without it
		ok     bool
	}{
		{"choice 1 unfinished at [DONE]", []string{both, firstDone}, "[DONE]", false},
		{"choice 1 unfinished at EOF", []string{both, firstDone}, "", false},
		{"choice 0 unfinished at [DONE]", []string{both, otherDone}, "[DONE]", false},
		{"both finished", []string{both, firstDone, otherDone}, "[DONE]", true},
		{"both finished, other first", []string{both, otherDone, firstDone}, "", true},
	} {
		d := NewStreamDecoder()
		for _, ch := range c.chunks {
			if _, err := d.Decode([]byte(ch)); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		var evs []llm.Event
		var err error
		if c.end == "[DONE]" {
			evs, err = d.Decode([]byte(c.end))
		} else {
			evs, err = d.Close()
		}
		var le *llm.Error
		switch {
		case c.ok && (err != nil || len(evs) == 0 || evs[len(evs)-1].Type != llm.EventFinish):
			t.Errorf("%s: events %+v, err %v; want a finish", c.name, evs, err)
		case !c.ok && (!errors.As(err, &le) || le.Code != llm.CodeUpstream):
			t.Errorf("%s: events %+v, err %v; want an upstream error", c.name, evs, err)
		}
	}
}

// Upstream bytes that are not JSON become an upstream error that can still be sent to core: the
// error event must encode, with the bytes kept in raw.
func TestMalformedUpstreamGivesError(t *testing.T) {
	const garbage = "{not json"
	_, respErr := DecodeResponse([]byte(garbage))
	_, chunkErr := NewStreamDecoder().Decode([]byte(garbage))
	for name, err := range map[string]error{"response": respErr, "stream chunk": chunkErr} {
		var le *llm.Error
		if !errors.As(err, &le) || le.Code != llm.CodeUpstream {
			t.Errorf("%s: err = %v, want an upstream error", name, err)
			continue
		}
		frame, err := json.Marshal(llm.Event{Type: llm.EventError, Error: le})
		if err != nil {
			t.Errorf("%s: error event does not encode: %v", name, err)
			continue
		}
		var back llm.Event
		var raw string
		if err := json.Unmarshal(frame, &back); err != nil || json.Unmarshal(back.Error.Raw, &raw) != nil || raw != garbage {
			t.Errorf("%s: raw = %s, want the upstream bytes as a JSON string", name, back.Error.Raw)
		}
	}
}

// A 200 body is a reply only if it has the shape of a completion: an object whose choices hold
// choice 0 with a message. Anything else, including an error object sent with status 200, is an
// upstream error rather than an empty successful answer.
func TestDecodeResponseNeedsACompletion(t *testing.T) {
	for _, c := range []struct{ name, body, msg, code string }{
		{"empty object", `{}`, "", ""},
		{"null", `null`, "", ""},
		{"array", `[]`, "", ""},
		{"no choices", `{"id":"x","choices":[]}`, "", ""},
		{"no first choice", `{"choices":[{"index":1,"message":{"role":"assistant","content":"hi"}}]}`, "", ""},
		{"choice without message", `{"choices":[{"index":0,"finish_reason":"stop"}]}`, "", ""},
		{"error object", `{"error":{"message":"The server is overloaded","type":"server_error","code":"server_busy"}}`, "The server is overloaded", "server_busy"},
	} {
		evs, err := DecodeResponse([]byte(c.body))
		var le *llm.Error
		if !errors.As(err, &le) || le.Code != llm.CodeUpstream {
			t.Errorf("%s: events %+v, err %v; want an upstream error", c.name, evs, err)
			continue
		}
		if c.msg != "" && (le.Message != c.msg || le.ProviderCode != c.code) {
			t.Errorf("%s: error = %+v, want message %q and code %q", c.name, le, c.msg, c.code)
		}
	}
	for _, body := range []string{
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"content_filter"}],"error":null}`,
	} {
		if _, err := DecodeResponse([]byte(body)); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}

// A streamed reply keeps what the internal model has no field for: reply-level fields on every
// chunk, choices after the first, and usage fields such as a vendor cost.
func TestStreamKeepsUpstreamFields(t *testing.T) {
	head := `"id":"x","object":"chat.completion.chunk","created":5,"model":"up","system_fingerprint":"fp_1"`
	chunks := []string{
		`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{"content":"B"},"finish_reason":null,"logprobs":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{},"finish_reason":"length"}]}`,
		`{` + head + `,"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"cost":0.5}}`,
		`[DONE]`,
	}
	var b bytes.Buffer
	w := NewStreamWriter(&b, true)
	d := NewStreamDecoder()
	var seq llm.Sequence
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if err := seq.Check(ev); err != nil {
				t.Fatalf("decoder broke the event rules: %v", err)
			}
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The first choice's finish chunk comes at the end, with the finish event; the other
	// choices' chunks are passed on as they arrive.
	want := []string{
		`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{"content":"B"},"finish_reason":null,"logprobs":null}]}`,
		`{` + head + `,"choices":[{"index":1,"delta":{},"finish_reason":"length"}]}`,
		`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{` + head + `,"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"cost":0.5}}`,
		`[DONE]`,
	}
	var got []string
	for _, ev := range strings.Split(strings.TrimSuffix(b.String(), "\n\n"), "\n\n") {
		got = append(got, strings.TrimPrefix(ev, "data: "))
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d:\n%s", len(got), len(want), b.String())
	}
	for i := range want {
		if want[i] == "[DONE]" {
			if got[i] != want[i] {
				t.Errorf("chunk %d = %s, want [DONE]", i, got[i])
			}
			continue
		}
		var g, w any
		if err := json.Unmarshal([]byte(got[i]), &g); err != nil {
			t.Fatalf("chunk %d is not JSON: %s", i, got[i])
		}
		_ = json.Unmarshal([]byte(want[i]), &w)
		if !reflect.DeepEqual(g, w) {
			t.Errorf("chunk %d:\n got %s\nwant %s", i, got[i], want[i])
		}
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

// Putting streamed extra choices together takes work in proportion to the fragments: the
// reviewer's case, one fragment with 4,000 tool calls, took about 16 million allocations because
// every call searched the calls before it. Doubling the calls must not much more than double the
// allocations, whether they come in one fragment or one per fragment.
func TestAggregatorMergesExtraChoicesInLinearWork(t *testing.T) {
	call := func(i int) string {
		return fmt.Sprintf(`{"index":%d,"id":"call_%d","type":"function","function":{"name":"f","arguments":"{}"}}`, i, i)
	}
	native := func(calls ...string) llm.Event {
		return llm.Event{Type: llm.EventNative, Raw: json.RawMessage(`{"choices":[{"index":1,"delta":{"tool_calls":[` + strings.Join(calls, ",") + `]}}]}`)}
	}
	shapes := map[string]func(n int) []llm.Event{
		"one fragment": func(n int) []llm.Event {
			calls := make([]string, n)
			for i := range calls {
				calls[i] = call(i)
			}
			return []llm.Event{native(calls...)}
		},
		"one call per fragment": func(n int) []llm.Event {
			evs := make([]llm.Event, n)
			for i := range evs {
				evs[i] = native(call(i))
			}
			return evs
		},
	}
	for name, events := range shapes {
		t.Run(name, func(t *testing.T) {
			allocs := func(n int) float64 {
				evs := events(n)
				return testing.AllocsPerRun(1, func() {
					a := NewAggregator()
					a.Add(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Model: "m", Format: llm.FormatOpenAIChat}})
					for _, ev := range evs {
						a.Add(ev)
					}
				})
			}
			small, large := allocs(2000), allocs(4000)
			if large > 3*small {
				t.Errorf("%.0f allocations for 2,000 calls, %.0f for 4,000: the work grows faster than the calls", small, large)
			}
		})
	}
}

// Joining streamed text or tool arguments takes work in proportion to the bytes. Joining each
// delta onto the string so far copies about 190 MiB for 2,000 deltas of 100 bytes, where the
// joined text is 200 KB.
func TestAggregatorJoinsStreamedStringsInLinearBytes(t *testing.T) {
	const deltas, size = 2000, 100
	const maxAlloc = 32 << 20
	piece := func(i int) string { return fmt.Sprintf("%0*d", size, i) }
	var joined strings.Builder
	for i := range deltas {
		joined.WriteString(piece(i))
	}
	want := joined.String()
	start := llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Model: "m", Format: llm.FormatOpenAIChat}}
	// feed adds the events and returns the response body and the bytes the Add calls allocated.
	feed := func(t *testing.T, evs []llm.Event) (respBody, uint64) {
		t.Helper()
		a := NewAggregator()
		a.Add(start)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for _, ev := range evs {
			a.Add(ev)
		}
		runtime.ReadMemStats(&after)
		b, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		var body respBody
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, b)
		}
		return body, after.TotalAlloc - before.TotalAlloc
	}
	check := func(t *testing.T, alloc uint64) {
		t.Helper()
		t.Logf("allocated %d bytes for %d deltas of %d bytes", alloc, deltas, size)
		if alloc > maxAlloc {
			t.Errorf("allocated %d bytes (over the %d limit) for %d deltas of %d bytes; the joined text is %d bytes", alloc, maxAlloc, deltas, size, len(want))
		}
	}
	t.Run("tool arguments", func(t *testing.T) {
		evs := []llm.Event{{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_a", Name: "f"}}}
		for i := range deltas {
			evs = append(evs, llm.Event{Type: llm.EventToolInputDelta, Index: 0, Delta: piece(i)})
		}
		body, alloc := feed(t, evs)
		if len(body.Choices) != 1 || len(body.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("want one choice with one tool call, got %+v", body.Choices)
		}
		if got := body.Choices[0].Message.ToolCalls[0].Function.Arguments; got != want {
			t.Errorf("arguments are %d bytes, want the %d joined bytes", len(got), len(want))
		}
		check(t, alloc)
	})
	t.Run("extra choice text", func(t *testing.T) {
		var evs []llm.Event
		for i := range deltas {
			evs = append(evs, llm.Event{Type: llm.EventNative, Raw: json.RawMessage(`{"choices":[{"index":1,"delta":{"content":"` + piece(i) + `"}}]}`)})
		}
		body, alloc := feed(t, evs)
		if len(body.Choices) != 2 || body.Choices[1].Message.Content == nil {
			t.Fatalf("want an extra choice with content, got %+v", body.Choices)
		}
		if got := *body.Choices[1].Message.Content; got != want {
			t.Errorf("extra choice content is %d bytes, want the %d joined bytes", len(got), len(want))
		}
		check(t, alloc)
	})
}

// respBody is the part of a Chat Completions response the aggregator tests read.
type respBody struct {
	Choices []struct {
		Message struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}
