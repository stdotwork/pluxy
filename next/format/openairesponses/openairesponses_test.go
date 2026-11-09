package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// A Responses request translated to Chat Completions reports each thing Chat Completions has no
// place for, by its path in the client's body, and carries over what it can.
func TestTranslationToChatReportsLosses(t *testing.T) {
	in := `{"model":"m","previous_response_id":"resp_1","store":true,"metadata":{"k":"v"},
	"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"},"x_schema":true}},
	"tools":[{"type":"function","name":"f","parameters":{"type":"object"},"strict":true},{"type":"web_search"}],
	"input":[
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"Read this."},{"type":"input_file","file_url":"https://example.com/a.pdf"}]},
	 {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Use f."}],"encrypted_content":"opaque"},
	 {"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}","status":"completed"},
	 {"type":"function_call_output","call_id":"call_1","output":"ok"},
	 {"type":"item_reference","id":"msg_9"}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range losses {
		got = append(got, l.Kind+" "+l.Path)
	}
	slices.Sort(got)
	want := []string{
		"approximated input[1]",              // reasoning summary becomes reasoning_content
		"dropped input[0].content[1]",        // a file by URL
		"dropped input[1].encrypted_content", // opaque reasoning
		"dropped input[4]",                   // an item reference
		"dropped metadata",
		"dropped previous_response_id",
		"dropped store",
		"dropped text",     // x_schema inside text.format
		"dropped tools[1]", // a hosted tool
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("losses:\n got %q\nwant %q", got, want)
	}
	wantBody := `{"model":"m","messages":[
	 {"role":"user","content":[{"type":"text","text":"Read this."}]},
	 {"role":"assistant","reasoning_content":"Use f.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","content":"ok","tool_call_id":"call_1"}],
	 "stream":false,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":true}}],
	 "response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"}}}}`
	var g, w any
	_ = json.Unmarshal(body, &g)
	_ = json.Unmarshal([]byte(wantBody), &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("body:\n got %s\nwant %s", body, wantBody)
	}
}

// Events of an upstream Responses stream keep the upstream event in Raw, events the model has no
// place for become native events, and the stream follows the protocol's ordering rules.
func TestStreamDecoderKeepsUpstreamEvents(t *testing.T) {
	stream := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":7,"status":"in_progress","model":"up","output":[]}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed"}}`,
		`{"type":"response.output_item.added","sequence_number":4,"output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":5,"output_index":1,"item_id":"fc_1","delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":6,"output_index":1,"item_id":"fc_1","arguments":"{}"}`,
		`{"type":"response.output_item.done","sequence_number":7,"output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":"{}"}}`,
		`{"type":"response.completed","sequence_number":8,"response":{"id":"resp_1","status":"completed","output":[{"type":"web_search_call"},{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":0}}}}`,
	}
	wantTypes := []string{llm.EventResponseStart, llm.EventNative, llm.EventNative, llm.EventNative, llm.EventItemStart,
		llm.EventToolInputDelta, llm.EventNative, llm.EventItemEnd, llm.EventFinish}
	dec := NewStreamDecoder()
	var seq llm.Sequence
	var last llm.Event
	for i, data := range stream {
		evs, err := dec.Decode([]byte(data))
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if len(evs) != 1 {
			t.Fatalf("event %d gave %d model events, want 1", i, len(evs))
		}
		ev := evs[0]
		if ev.Type != wantTypes[i] {
			t.Errorf("event %d: type %q, want %q", i, ev.Type, wantTypes[i])
		}
		if string(ev.Raw) != data {
			t.Errorf("event %d: raw %s, want the upstream event", i, ev.Raw)
		}
		if err := seq.Check(ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		last = ev
	}
	if last.Finish.Reason != llm.FinishToolCalls {
		t.Errorf("finish reason %q, want tool_calls", last.Finish.Reason)
	}
	if u := last.Usage; u == nil || *u.InputTokens != 3 || *u.OutputTokens != 2 || *u.CachedInputTokens != 1 {
		t.Errorf("usage %+v", last.Usage)
	}
}

// A stream that ends without a terminal event is an error, and response.failed is an error that
// keeps the upstream event.
func TestStreamDecoderFailures(t *testing.T) {
	dec := NewStreamDecoder()
	if _, err := dec.Decode([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Close(); err == nil {
		t.Error("Close before a terminal event: no error")
	}

	dec = NewStreamDecoder()
	if _, err := dec.Decode([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r"}}`)); err != nil {
		t.Fatal(err)
	}
	failed := `{"type":"response.failed","sequence_number":1,"response":{"id":"r","status":"failed","error":{"code":"server_error","message":"boom"}}}`
	_, err := dec.Decode([]byte(failed))
	e, ok := err.(*llm.Error)
	if !ok || e.Message != "boom" || e.ProviderCode != "server_error" || string(e.Raw) != failed {
		t.Errorf("response.failed gave %v", err)
	}

	// A failed Response that reports usage keeps it on the error, so the tokens the upstream
	// counted are not lost with the reply.
	dec = NewStreamDecoder()
	_, err = dec.Decode([]byte(`{"type":"response.failed","response":{"id":"r","status":"failed","usage":{"input_tokens":9,"output_tokens":2},"error":{"code":"server_error","message":"boom"}}}`))
	if e, ok := err.(*llm.Error); !ok || e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 9 || e.Usage.OutputTokens == nil || *e.Usage.OutputTokens != 2 {
		t.Errorf("response.failed with usage gave %#v, want an error carrying 9 input and 2 output tokens", err)
	}
}

// A reply from a Chat upstream to a Responses client becomes the Responses event sequence, and
// what Chat had that a Response cannot hold is reported.
func TestStreamWriterRebuildsEvents(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	w := NewStreamWriter(&out, req)
	start := llm.ItemStart{Kind: llm.ItemMessage}
	for _, ev := range []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Created: 5, Format: llm.FormatOpenAIChat, Raw: json.RawMessage(`{"system_fingerprint":"fp"}`)}},
		{Type: llm.EventItemStart, Index: 0, Item: &start},
		{Type: llm.EventTextDelta, Index: 0, Delta: "Hel"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishLength}},
	} {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	var types []string
	for _, line := range strings.Split(out.String(), "\n") {
		if typ, ok := strings.CutPrefix(line, "event: "); ok {
			types = append(types, typ)
		}
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.incomplete"}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("events:\n got %q\nwant %q", types, want)
	}
	if !strings.Contains(out.String(), `"incomplete_details":{"reason":"max_output_tokens"}`) {
		t.Errorf("no incomplete_details in %s", out.String())
	}
	if l := w.Losses(); len(l) != 1 || l[0].Path != "response.system_fingerprint" {
		t.Errorf("losses %+v, want system_fingerprint dropped", l)
	}
}

// A failure after the reply started reaches a Responses client as an error event with code,
// message and param at its top level, as OpenAI's error event has them.
func TestStreamWriterErrorEvent(t *testing.T) {
	req, _ := ParseRequest([]byte(`{"model":"m","input":"hi","stream":true}`))
	var out strings.Builder
	w := NewStreamWriter(&out, req)
	for _, ev := range []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUpstream, Message: "boom", ProviderCode: "server_error"}},
	} {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	data, _ := strings.CutPrefix(lines[len(lines)-1], "data: ")
	var e struct {
		Type    string  `json:"type"`
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
	}
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != "error" || e.Message != "boom" || e.Code != "server_error" || !strings.Contains(data, `"param":null`) {
		t.Errorf("error event %s", data)
	}
}

// A Responses client gets an upstream Responses stream's events unchanged and once each, also
// when the stream does not open with response.created, carries items the model has no place
// for, or opens with its terminal event.
func TestNativeStreamPassesThrough(t *testing.T) {
	for name, stream := range map[string][]string{
		"no response.created": {
			`{"type":"response.in_progress","sequence_number":0,"response":{"id":"r","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
			`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed"}}`,
			`{"type":"response.completed","sequence_number":3,"response":{"id":"r","status":"completed","output":[{"id":"ws_1","type":"web_search_call","status":"completed"}]}}`,
		},
		"only response.completed": {
			`{"type":"response.completed","sequence_number":0,"response":{"id":"r","status":"completed","output":[{"id":"m","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}}`,
		},
		"only response.failed": {
			`{"type":"response.failed","sequence_number":0,"response":{"id":"r","status":"failed","error":{"code":"server_error","message":"boom"}}}`,
		},
	} {
		t.Run(name, func(t *testing.T) { checkPassthrough(t, stream) })
	}
}

func checkPassthrough(t *testing.T, stream []string) {
	req, _ := ParseRequest([]byte(`{"model":"m","input":"hi","stream":true}`))
	var out strings.Builder
	w := NewStreamWriter(&out, req)
	dec := NewStreamDecoder()
	var seq llm.Sequence
	for _, data := range stream {
		evs, err := dec.Decode([]byte(data))
		if e, ok := err.(*llm.Error); ok {
			evs, err = append(evs, llm.Event{Type: llm.EventError, Error: e}), nil
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if err := seq.Check(ev); err != nil {
				t.Fatal(err)
			}
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	var want strings.Builder
	for _, data := range stream {
		var e struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(data), &e)
		want.WriteString("event: " + e.Type + "\ndata: " + data + "\n\n")
	}
	if out.String() != want.String() {
		t.Errorf("client got:\n%s\nwant:\n%s", out.String(), want.String())
	}
}

// Encrypted reasoning and annotations are listed by their path in the Response, from a stream's
// item event or from the whole Response.
func TestOpaqueOutput(t *testing.T) {
	item := `{"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","encrypted_content":"gAAA","summary":[]}}`
	if got := OpaqueOutput(json.RawMessage(item)); !reflect.DeepEqual(got, []string{"output[2].encrypted_content"}) {
		t.Errorf("item event: %q", got)
	}
	resp := `{"object":"response","output":[{"type":"reasoning","encrypted_content":""},
	 {"type":"message","content":[{"type":"output_text","text":"x","annotations":[{"type":"url_citation"}]},{"type":"output_text","annotations":[]}]}]}`
	if got := OpaqueOutput(json.RawMessage(resp)); !reflect.DeepEqual(got, []string{"output[1].content[0].annotations"}) {
		t.Errorf("response: %q", got)
	}
}

// Opaque reasoning data the aggregator keeps counts toward its size, which core bounds.
func TestAggregatorSizeCountsOpaque(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAggregator(req)
	a.Add(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Format: llm.FormatAnthropicMessages}})
	a.Add(llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}})
	before := a.Size()
	a.Add(llm.Event{Type: llm.EventItemEnd, Index: 0, Opaque: &llm.Opaque{Format: llm.FormatAnthropicMessages, Data: strings.Repeat("E", 1<<20)}})
	if got := a.Size() - before; got < 1<<20 {
		t.Errorf("size grew by %d for 1 MiB of opaque data", got)
	}
}

// A reply with more distinct losses than llm.MaxLosses lists the first MaxLosses, then one entry
// that says more were left out, and nothing after it.
func TestStreamWriterBoundsLosses(t *testing.T) {
	raw := map[string]int{}
	for i := range 150 {
		raw[fmt.Sprintf("k%03d", i)] = i
	}
	rb, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := NewStreamWriter(&out, llm.Request{})
	if err := w.Write(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatOpenAIChat, Raw: rb}}); err != nil {
		t.Fatal(err)
	}
	losses := w.Losses()
	if len(losses) != llm.MaxLosses+1 {
		t.Fatalf("got %d losses, want %d: %d listed and one saying more were left out", len(losses), llm.MaxLosses+1, llm.MaxLosses)
	}
	if losses[0].Path != "response.k000" {
		t.Errorf("first loss is at %q, want response.k000", losses[0].Path)
	}
	want := llm.Loss{Kind: llm.LossDropped, Path: "...", Detail: "more losses not listed"}
	if got := losses[len(losses)-1]; got != want {
		t.Errorf("last loss is %+v, want %+v", got, want)
	}
}

// Decoding a non-streamed reply takes work in proportion to its bytes: joining each text part onto
// the text so far copies about 200 MiB for 2,000 parts of 100 bytes, where the joined text is
// 200 KB.
func TestDecodeResponseJoinsTextPartsInLinearBytes(t *testing.T) {
	const parts, size = 2000, 100
	const maxAlloc = 32 << 20
	part := func(i int) string { return fmt.Sprintf("%0*d", size, i) }
	var joined strings.Builder
	list := make([]string, parts)
	for i := range parts {
		joined.WriteString(part(i))
		list[i] = fmt.Sprintf(`{"type":"output_text","text":%q}`, part(i))
	}
	want := joined.String()
	summaries := strings.ReplaceAll(strings.Join(list, ","), "output_text", "summary_text")
	cases := map[string]struct{ item, delta string }{
		"message":   {`{"type":"message","role":"assistant","content":[` + strings.Join(list, ",") + `]}`, llm.EventTextDelta},
		"reasoning": {`{"type":"reasoning","summary":[` + summaries + `]}`, llm.EventReasoningDelta},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"id":"r1","status":"completed","model":"m","output":[` + c.item + `]}`)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			evs, err := DecodeResponse(body)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			alloc := after.TotalAlloc - before.TotalAlloc
			t.Logf("allocated %d bytes for %d parts of %d bytes", alloc, parts, size)
			var got string
			for _, ev := range evs {
				if ev.Type == c.delta {
					got = ev.Delta
				}
			}
			if got != want {
				t.Errorf("delta is %d bytes, want the %d joined bytes", len(got), len(want))
			}
			if alloc > maxAlloc {
				t.Errorf("allocated %d bytes (over the %d limit) for %d parts of %d bytes; the joined text is %d bytes", alloc, maxAlloc, parts, size, len(want))
			}
		})
	}
}

// writeNative runs an upstream Responses stream through the decoder and a stream writer and
// returns the data of each event the client got.
func writeNative(t *testing.T, stream []string) []string {
	t.Helper()
	req, _ := ParseRequest([]byte(`{"model":"m","input":"hi","stream":true}`))
	var out strings.Builder
	w := NewStreamWriter(&out, req)
	dec := NewStreamDecoder()
	for _, data := range stream {
		evs, err := dec.Decode([]byte(data))
		if e, ok := err.(*llm.Error); ok {
			evs, err = append(evs, llm.Event{Type: llm.EventError, Error: e}), nil
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	var datas []string
	for _, line := range strings.Split(out.String(), "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			datas = append(datas, d)
		}
	}
	return datas
}

// A passed-through response.completed whose output is empty, null or absent gets the items the
// stream's output_item.done events finished, by output_index and then those without one, as
// today's core rebuilds it; one with output is left alone, and so is response.incomplete.
func TestNativeStreamRepairsEmptyOutput(t *testing.T) {
	done := []string{
		`{"type":"response.output_item.done","sequence_number":1,"output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}"}}`,
		`{"type":"response.output_item.done","sequence_number":2,"item":{"type":"message","id":"m_1","role":"assistant","content":[]}}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":2,"item":{"id":"no_type"}}`,
	}
	rebuilt := `[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}"},{"type":"message","id":"m_1","role":"assistant","content":[]}]`
	for _, c := range []struct{ name, last, want string }{
		{"empty", `{"type":"response.completed","sequence_number":5,"response":{"id":"r","status":"completed","output":[]}}`, rebuilt},
		{"null", `{"type":"response.completed","sequence_number":5,"response":{"id":"r","status":"completed","output":null}}`, rebuilt},
		{"absent", `{"type":"response.completed","sequence_number":5,"response":{"id":"r","status":"completed"}}`, rebuilt},
		{"present", `{"type":"response.completed","sequence_number":5,"response":{"id":"r","status":"completed","output":[{"type":"message","id":"m_2"}]}}`, `[{"type":"message","id":"m_2"}]`},
		{"incomplete", `{"type":"response.incomplete","sequence_number":5,"response":{"id":"r","status":"incomplete","output":[]}}`, `[]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			stream := append([]string{`{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress","output":[]}}`}, done...)
			got := writeNative(t, append(stream, c.last))
			var ev struct {
				Response struct {
					Output json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(got[len(got)-1]), &ev); err != nil {
				t.Fatal(err)
			}
			if c.name == "absent" || c.want != rebuilt {
				// The events before the last pass through unchanged either way.
				for i, d := range append(stream[:0:0], stream...) {
					if got[i] != d {
						t.Errorf("event %d = %s, want %s", i, got[i], d)
					}
				}
			}
			var gotOut, wantOut any
			_ = json.Unmarshal(ev.Response.Output, &gotOut)
			_ = json.Unmarshal([]byte(c.want), &wantOut)
			if !reflect.DeepEqual(gotOut, wantOut) {
				t.Errorf("output = %s, want %s", ev.Response.Output, c.want)
			}
		})
	}
}
