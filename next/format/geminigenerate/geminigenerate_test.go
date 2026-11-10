package geminigenerate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// sameJSON fails unless got and want hold the same JSON value; key order and spacing do not count.
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

func mustParse(t *testing.T, body string) llm.Request {
	t.Helper()
	req, err := ParseRequest("m", false, []byte(body))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	return req
}

func wantInvalidRequest(t *testing.T, err error) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an *llm.Error", err)
	}
	if e.Code != llm.CodeInvalidRequest {
		t.Errorf("code %q, want %q: %v", e.Code, llm.CodeInvalidRequest, e)
	}
}

func wantTokens(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s not reported, want %d", name, want)
	} else if *got != want {
		t.Errorf("%s = %d, want %d", name, *got, want)
	}
}

// writeStream runs events through a StreamWriter and returns the SSE text it wrote.
func writeStream(t *testing.T, events []llm.Event) string {
	t.Helper()
	var buf bytes.Buffer
	w := NewStreamWriter(&buf)
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatalf("Write(%s): %v", ev.Type, err)
		}
	}
	return buf.String()
}

// dataLines returns the payload of each SSE event in out, which must hold only data events.
func dataLines(t *testing.T, out string) []string {
	t.Helper()
	var got []string
	for _, block := range strings.Split(strings.TrimSuffix(out, "\n\n"), "\n\n") {
		data, ok := strings.CutPrefix(block, "data: ")
		if !ok {
			t.Fatalf("event %q is not a data event", block)
		}
		got = append(got, data)
	}
	return got
}

// Gemini's snake_case spellings read the same as the camelCase ones, and the body is kept as sent.
func TestParseRequestReadsSnakeCase(t *testing.T) {
	const body = `{"system_instruction":{"parts":[{"text":"S"}]},"contents":[{"role":"user","parts":[{"text":"hi"},{"inline_data":{"mime_type":"image/png","data":"aGk="}}]},{"role":"model","parts":[{"function_call":{"name":"f","args":{"a_b":1}}}]}],"generation_config":{"max_output_tokens":7}}`
	req := mustParse(t, body)
	if req.Native == nil {
		t.Fatalf("Native is nil, want the body kept")
	}
	if string(req.Native.Body) != body {
		t.Errorf("Native.Body = %s, want the input bytes", req.Native.Body)
	}
	if len(req.Items) != 3 {
		t.Fatalf("items = %d, want 3 (system, user, tool call): %+v", len(req.Items), req.Items)
	}
	if sys := req.Items[0]; sys.Role != llm.RoleSystem || len(sys.Content) != 1 || sys.Content[0].Text != "S" {
		t.Errorf("system item = %+v", sys)
	}
	user := req.Items[1]
	if user.Role != llm.RoleUser || len(user.Content) != 2 ||
		user.Content[0].Type != llm.PartText || user.Content[0].Text != "hi" ||
		user.Content[1].Type != llm.PartImage || user.Content[1].URL != "data:image/png;base64,aGk=" {
		t.Errorf("user item = %+v, want text hi and image data:image/png;base64,aGk=", user)
	}
	if call := req.Items[2]; call.Type != llm.ItemTypeToolCall || call.Name != "f" || call.Arguments != `{"a_b":1}` {
		t.Errorf("tool call = %+v, want f with arguments {\"a_b\":1}", call)
	}
	if req.Options.MaxOutputTokens == nil || *req.Options.MaxOutputTokens != 7 {
		t.Errorf("MaxOutputTokens = %v, want 7", req.Options.MaxOutputTokens)
	}
}

// A thought signature stays on the part it came with.
func TestThoughtSignatureStaysOnItsPart(t *testing.T) {
	req := mustParse(t, `{"contents":[{"role":"model","parts":[{"text":"A","thoughtSignature":"sig1"},{"text":"B"}]}]}`)
	if len(req.Items) != 1 || len(req.Items[0].Content) != 2 {
		t.Fatalf("items = %+v, want one message of two parts", req.Items)
	}
	a, b := req.Items[0].Content[0], req.Items[0].Content[1]
	if a.Origin == nil {
		t.Fatalf("first part has no origin, want its thought signature kept")
	}
	sameJSON(t, "first part's Origin.Raw", a.Origin.Raw, `{"thoughtSignature":"sig1"}`)
	if b.Origin != nil && len(b.Origin.Raw) > 0 {
		t.Errorf("second part Origin.Raw = %s, want none", b.Origin.Raw)
	}
}

// Results without ids pair with calls of the same name in order; an id is kept as sent.
func TestCallsAndResultsPairWithoutIDs(t *testing.T) {
	t.Run("ordered pairing", func(t *testing.T) {
		req := mustParse(t, `{"contents":[`+
			`{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":1}}},{"functionCall":{"name":"lookup","args":{"q":2}}}]},`+
			`{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"r":1}}},{"functionResponse":{"name":"lookup","response":{"r":2}}}]}]}`)
		if len(req.Items) != 4 {
			t.Fatalf("items = %d, want 4 (two calls, two results): %+v", len(req.Items), req.Items)
		}
		c1, c2 := req.Items[0].CallID, req.Items[1].CallID
		if c1 == "" || c2 == "" || c1 == c2 {
			t.Errorf("call ids %q and %q, want two different non-empty ids", c1, c2)
		}
		if got := req.Items[2].CallID; got != c1 {
			t.Errorf("first result CallID = %q, want the first call's %q", got, c1)
		}
		if got := req.Items[3].CallID; got != c2 {
			t.Errorf("second result CallID = %q, want the second call's %q", got, c2)
		}
	})
	t.Run("id kept without a matching call", func(t *testing.T) {
		req := mustParse(t, `{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"x","name":"other","response":{}}}]}]}`)
		if len(req.Items) != 1 || req.Items[0].CallID != "x" {
			t.Errorf("items = %+v, want one tool result with CallID x", req.Items)
		}
	})
	t.Run("made-up id without a waiting call", func(t *testing.T) {
		req := mustParse(t, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]}`)
		if len(req.Items) != 1 || !strings.HasPrefix(req.Items[0].CallID, "call_") {
			t.Errorf("items = %+v, want one tool result with a CallID starting call_", req.Items)
		}
	})
}

// A function response's output is its response.content when it has one, else the whole response.
func TestFunctionResponseOutput(t *testing.T) {
	req := mustParse(t, `{"contents":[`+
		`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"content":{"ok":true}}}}]},`+
		`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"temp":18}}}]}]}`)
	if len(req.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(req.Items))
	}
	if got := req.Items[0].Output; got != `{"ok":true}` {
		t.Errorf("content response Output = %s, want {\"ok\":true}", got)
	}
	if got := req.Items[1].Output; got != `{"temp":18}` {
		t.Errorf("plain response Output = %s, want {\"temp\":18}", got)
	}
}

// A thought is reasoning the model showed, kept whole as a native item, not as a message part.
func TestThoughtPartIsNativeItem(t *testing.T) {
	req := mustParse(t, `{"contents":[{"role":"model","parts":[{"text":"hmm","thought":true},{"text":"ok"}]}]}`)
	if len(req.Items) != 2 {
		t.Fatalf("items = %+v, want a native item then a message", req.Items)
	}
	if req.Items[0].Type != llm.ItemTypeNative {
		t.Errorf("first item type = %q, want %q", req.Items[0].Type, llm.ItemTypeNative)
	}
	if m := req.Items[1]; m.Type != llm.ItemTypeMessage || len(m.Content) != 1 || m.Content[0].Text != "ok" {
		t.Errorf("second item = %+v, want a message holding only ok", m)
	}
}

// Gemini's upper-case schema types become JSON Schema's lower case; a property named type stays a
// property; parametersJsonSchema is JSON Schema already and is kept byte for byte.
func TestToolParametersAsJSONSchema(t *testing.T) {
	req := mustParse(t, `{"tools":[{"functionDeclarations":[`+
		`{"name":"p","parameters":{"type":"OBJECT","properties":{"type":{"type":"STRING"}}}},`+
		`{"name":"q","parametersJsonSchema":{"type":"OBJECT","properties":{"x":{"type":"STRING"}}}}]}]}`)
	if len(req.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(req.Tools))
	}
	sameJSON(t, "parameters of p", req.Tools[0].Parameters, `{"type":"object","properties":{"type":{"type":"string"}}}`)
	const kept = `{"type":"OBJECT","properties":{"x":{"type":"STRING"}}}`
	if got := string(req.Tools[1].Parameters); got != kept {
		t.Errorf("parametersJsonSchema = %s, want it byte for byte: %s", got, kept)
	}
}

// functionCallingConfig maps to a tool choice; ANY with several names is required, and the names
// stay in the request's Origin.
func TestToolConfigChoice(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  string
		want llm.ToolChoice
	}{
		{"AUTO", `{"functionCallingConfig":{"mode":"AUTO"}}`, llm.ToolChoice{Mode: llm.ToolChoiceAuto}},
		{"NONE", `{"functionCallingConfig":{"mode":"NONE"}}`, llm.ToolChoice{Mode: llm.ToolChoiceNone}},
		{"ANY with one name", `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}}`, llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "f"}},
		{"ANY with two names", `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f","g"]}}`, llm.ToolChoice{Mode: llm.ToolChoiceRequired}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mustParse(t, `{"contents":[],"toolConfig":`+tc.cfg+`}`)
			if req.Options.ToolChoice == nil || *req.Options.ToolChoice != tc.want {
				t.Errorf("ToolChoice = %+v, want %+v", req.Options.ToolChoice, tc.want)
			}
			if tc.name == "ANY with two names" && !strings.Contains(string(req.Origin.Raw), `"allowedFunctionNames":["f","g"]`) {
				t.Errorf("Origin.Raw = %s, want the allowedFunctionNames kept", req.Origin.Raw)
			}
		})
	}
}

// A thinking budget maps to the effort it falls in, -1 to auto, and thinkingLevel to itself.
func TestThinkingConfigEffort(t *testing.T) {
	for _, tc := range []struct {
		cfg, want string
	}{
		{`{"thinkingBudget":1024}`, "low"},
		{`{"thinkingBudget":0}`, "none"},
		{`{"thinkingBudget":-1}`, "auto"},
		{`{"thinkingLevel":"HIGH"}`, "high"},
	} {
		req := mustParse(t, `{"contents":[],"generationConfig":{"thinkingConfig":`+tc.cfg+`}}`)
		if req.Options.ReasoningEffort != tc.want {
			t.Errorf("thinkingConfig %s: ReasoningEffort = %q, want %q", tc.cfg, req.Options.ReasoningEffort, tc.want)
		}
	}
}

func TestParseRequestRejects(t *testing.T) {
	_, err := ParseRequest("m", false, []byte(`{"contents":`))
	wantInvalidRequest(t, err)

	_, err = ParseRequest("m", false, []byte(`{"contents":[{"role":"system","parts":[{"text":"x"}]}]}`))
	wantInvalidRequest(t, err)
}

// Every chunk rides in Raw on the first event it produces; the finish and usage come from Close.
func TestStreamDecoderUsageFinishAndRawChunks(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"index":0}],"modelVersion":"v","responseId":"r"}`,
		`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"s"}]},"finishReason":"SAFETY","index":0}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":5,"toolUsePromptTokenCount":2,"totalTokenCount":20}}`,
	}
	d := NewStreamDecoder()
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if len(evs) == 0 {
			t.Fatalf("Decode returned no events for %s", c)
		}
		if string(evs[0].Raw) != c {
			t.Errorf("first event of the chunk has Raw %s, want the chunk", evs[0].Raw)
		}
		for _, ev := range evs[1:] {
			if len(ev.Raw) > 0 {
				t.Errorf("event %s after the first also carries Raw", ev.Type)
			}
		}
	}
	end, err := d.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(end) != 1 || end[0].Type != llm.EventFinish {
		t.Fatalf("Close = %+v, want one finish event", end)
	}
	f := end[0]
	if f.Finish == nil || f.Finish.Reason != llm.FinishContentFilter || f.Finish.RawReason != "SAFETY" {
		t.Errorf("finish = %+v, want content_filter with raw reason SAFETY", f.Finish)
	}
	if f.Usage == nil {
		t.Fatalf("finish carries no usage")
	}
	wantTokens(t, "input tokens", f.Usage.InputTokens, 12)
	wantTokens(t, "cached input tokens", f.Usage.CachedInputTokens, 4)
	wantTokens(t, "output tokens", f.Usage.OutputTokens, 8)
	wantTokens(t, "reasoning tokens", f.Usage.ReasoningTokens, 5)
}

func TestStreamDecoderFinishReasons(t *testing.T) {
	const call = `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}}]},"index":0}]}`
	for _, tc := range []struct {
		name   string
		chunks []string
		reason string
		raw    string
	}{
		{"RECITATION", []string{`{"candidates":[{"finishReason":"RECITATION","index":0}]}`}, llm.FinishContentFilter, "RECITATION"},
		{"MAX_TOKENS", []string{`{"candidates":[{"finishReason":"MAX_TOKENS","index":0}]}`}, llm.FinishLength, "MAX_TOKENS"},
		{"STOP after a function call", []string{call, `{"candidates":[{"finishReason":"STOP","index":0}]}`}, llm.FinishToolCalls, "STOP"},
		{"OTHER", []string{`{"candidates":[{"finishReason":"OTHER","index":0}]}`}, llm.FinishOther, "OTHER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewStreamDecoder()
			for _, c := range tc.chunks {
				if _, err := d.Decode([]byte(c)); err != nil {
					t.Fatalf("Decode: %v", err)
				}
			}
			end, err := d.Close()
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
			if len(end) == 0 || end[len(end)-1].Type != llm.EventFinish {
				t.Fatalf("Close = %+v, want a finish event", end)
			}
			f := end[len(end)-1].Finish
			if f == nil || f.Reason != tc.reason || f.RawReason != tc.raw {
				t.Errorf("finish = %+v, want reason %s with raw reason %s", f, tc.reason, tc.raw)
			}
		})
	}
}

func TestStreamDecoderFailures(t *testing.T) {
	d := NewStreamDecoder()
	if _, err := d.Decode([]byte(`{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"index":0}]}`)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	_, err := d.Close()
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
		t.Errorf("Close without a finish reason: %v, want an upstream error", err)
	}

	_, err = NewStreamDecoder().Decode([]byte(`{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED"}}`))
	if !errors.As(err, &e) {
		t.Fatalf("error chunk: %v, want an *llm.Error", err)
	}
	if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || e.ProviderCode != "RESOURCE_EXHAUSTED" {
		t.Errorf("error = code %q scope %q provider code %q, want rate_limit on the model and RESOURCE_EXHAUSTED", e.Code, e.Scope, e.ProviderCode)
	}

	// A stream that fails after reporting usage keeps it on the error, whether the upstream sends
	// an error chunk or the stream ends without a finish reason.
	const withUsage = `{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2}}`
	d = NewStreamDecoder()
	_, _ = d.Decode([]byte(withUsage))
	_, err = d.Decode([]byte(`{"error":{"code":500,"message":"m","status":"INTERNAL"}}`))
	if !errors.As(err, &e) || e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 9 {
		t.Errorf("error chunk after usage: %#v, want an error carrying 9 input tokens", err)
	}
	d = NewStreamDecoder()
	_, _ = d.Decode([]byte(withUsage))
	_, err = d.Close()
	if !errors.As(err, &e) || e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 9 {
		t.Errorf("cut-short stream after usage: %#v, want an error carrying 9 input tokens", err)
	}
	if u := d.Usage(); u == nil || u.InputTokens == nil || *u.InputTokens != 9 {
		t.Errorf("Usage() = %#v, want 9 input tokens", u)
	}
}

// A blocked prompt in a non-streamed reply ends with a content_filter finish, and the whole body
// rides on the first event.
func TestDecodeResponseBlockedPrompt(t *testing.T) {
	const body = `{"promptFeedback":{"blockReason":"SAFETY"}}`
	evs, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if len(evs) < 2 {
		t.Fatalf("events = %+v, want at least a start and a finish", evs)
	}
	if string(evs[0].Raw) != body {
		t.Errorf("first event Raw = %s, want the whole body", evs[0].Raw)
	}
	last := evs[len(evs)-1]
	if last.Type != llm.EventFinish || last.Finish == nil || last.Finish.Reason != llm.FinishContentFilter || last.Finish.RawReason != "SAFETY" {
		t.Errorf("last event = %+v, want finish content_filter with raw reason SAFETY", last)
	}
}

// Events from a Chat Completions upstream are written as Gemini chunks: text and thoughts as they
// come, each function call whole in the last chunk with the finish reason and usage.
func TestStreamWriterFromChatUpstream(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "id1", Model: "m", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "think"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 1, Delta: "Hi"},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventItemStart, Index: 2, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "c1", Name: "f"}},
		{Type: llm.EventToolInputDelta, Index: 2, Delta: `{"a":`},
		{Type: llm.EventToolInputDelta, Index: 2, Delta: `1}`},
		{Type: llm.EventItemEnd, Index: 2},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}, Usage: &llm.Usage{
			InputTokens: llm.Int64(10), CachedInputTokens: llm.Int64(4), OutputTokens: llm.Int64(8), ReasoningTokens: llm.Int64(5)}},
	}
	got := dataLines(t, writeStream(t, events))
	want := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"think","thought":true}]},"index":0}],"modelVersion":"m","responseId":"id1"}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"index":0}],"modelVersion":"m","responseId":"id1"}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"c1","name":"f","args":{"a":1}}}]},"finishReason":"STOP","index":0}],` +
			`"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":5,"totalTokenCount":18},` +
			`"modelVersion":"m","responseId":"id1"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("%d data events, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		sameJSON(t, fmt.Sprintf("chunk %d", i), []byte(got[i]), want[i])
	}
}

// From a Gemini upstream, events that carry Raw are written as they came, compacted; others write
// nothing.
func TestStreamWriterFromGeminiUpstreamWritesRaw(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m", Format: llm.FormatGeminiGenerate}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "x", Raw: json.RawMessage(`{"candidates": [{"index": 0}]}`)},
		{Type: llm.EventTextDelta, Index: 0, Delta: "y"},
		{Type: llm.EventNative, Raw: json.RawMessage(`{"usageMetadata": {"promptTokenCount": 1}}`)},
	}
	const want = "data: {\"candidates\":[{\"index\":0}]}\n\n" + "data: {\"usageMetadata\":{\"promptTokenCount\":1}}\n\n"
	if got := writeStream(t, events); got != want {
		t.Errorf("stream:\n got %q\nwant %q", got, want)
	}
}

// The same events as the stream test, non-streamed, give one body: thought, text and the call in
// order, and the finish.
func TestAggregatorFromChatUpstream(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "id1", Model: "m", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "think"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 1, Delta: "Hi"},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventItemStart, Index: 2, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "c1", Name: "f"}},
		{Type: llm.EventToolInputDelta, Index: 2, Delta: `{"a":`},
		{Type: llm.EventToolInputDelta, Index: 2, Delta: `1}`},
		{Type: llm.EventItemEnd, Index: 2},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}, Usage: &llm.Usage{
			InputTokens: llm.Int64(10), CachedInputTokens: llm.Int64(4), OutputTokens: llm.Int64(8), ReasoningTokens: llm.Int64(5)}},
	}
	a := NewAggregator()
	for _, ev := range events {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	sameJSON(t, "response", body, `{"candidates":[{"content":{"role":"model","parts":[`+
		`{"text":"think","thought":true},{"text":"Hi"},{"functionCall":{"id":"c1","name":"f","args":{"a":1}}}]},`+
		`"finishReason":"STOP","index":0}],`+
		`"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":5,"totalTokenCount":18},`+
		`"modelVersion":"m","responseId":"id1"}`)
}

// A Gemini upstream's reply with one raw-carrying event is returned as the upstream sent it.
func TestAggregatorFromGeminiUpstreamReturnsRaw(t *testing.T) {
	const raw = `{"candidates": [{"index": 0, "content": {"parts": [{"text": "x"}]}}]}`
	a := NewAggregator()
	for _, ev := range []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m", Format: llm.FormatGeminiGenerate}},
		{Type: llm.EventNative, Raw: json.RawMessage(raw)},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop, RawReason: "STOP"}},
	} {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if string(body) != raw {
		t.Errorf("Response = %s, want the upstream's body unchanged: %s", body, raw)
	}
}

// A Google error keeps its status name and details; an error without one maps from its code.
func TestErrorResponseGoogleShape(t *testing.T) {
	const details = `[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]`
	raw := `{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED","details":` + details + `}}`
	st, body := ErrorResponse(&llm.Error{Code: llm.CodeRateLimit, Status: 429, Message: "m",
		ProviderCode: "RESOURCE_EXHAUSTED", Raw: json.RawMessage(raw)})
	if st != 429 {
		t.Errorf("status = %d, want 429", st)
	}
	sameJSON(t, "body", body, `{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED","details":`+details+`}}`)

	st, body = ErrorResponse(&llm.Error{Code: llm.CodeAuth})
	if st != 401 {
		t.Errorf("status = %d, want 401", st)
	}
	var got struct {
		Error struct {
			Code   int    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if got.Error.Code != 401 || got.Error.Status != "UNAUTHENTICATED" {
		t.Errorf("error = %+v, want code 401 and status UNAUTHENTICATED: %s", got.Error, body)
	}
}

// A response that holds more than content is sent whole, so fields such as error are not lost.
func TestFunctionResponseWithMoreThanContentIsWhole(t *testing.T) {
	req := mustParse(t, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f",`+
		`"response":{"content":"partial","error":"permission denied"}}}]}]}`)
	sameJSON(t, "Output", []byte(req.Items[0].Output), `{"content":"partial","error":"permission denied"}`)
}

// Gemini Schema becomes JSON Schema: nullable as a null type, int64 limits written as strings as
// numbers, nested schemas converted; example and default values are data and stay as they are.
func TestGeminiSchemaToJSONSchema(t *testing.T) {
	req := mustParse(t, `{"tools":[{"functionDeclarations":[{"name":"p","parameters":{"type":"OBJECT",`+
		`"properties":{"s":{"type":"STRING","nullable":true,"example":{"type":"STRING"}},`+
		`"a":{"type":"ARRAY","maxItems":"2","items":{"type":"INTEGER"},"default":[{"type":"STRING"}]},`+
		`"u":{"anyOf":[{"type":"NUMBER"},{"type":"BOOLEAN"}]}}}}]}]}`)
	sameJSON(t, "parameters", req.Tools[0].Parameters, `{"type":"object","properties":{`+
		`"s":{"type":["string","null"],"example":{"type":"STRING"}},`+
		`"a":{"type":"array","maxItems":2,"items":{"type":"integer"},"default":[{"type":"STRING"}]},`+
		`"u":{"anyOf":[{"type":"number"},{"type":"boolean"}]}}}`)
}

// ProtoJSON lets numbers be written as strings; they read the same.
func TestGenerationConfigQuotedNumbers(t *testing.T) {
	req := mustParse(t, `{"generationConfig":{"maxOutputTokens":"100","temperature":"0.5","thinkingConfig":{"thinkingBudget":"1024"}}}`)
	o := req.Options
	if o.MaxOutputTokens == nil || *o.MaxOutputTokens != 100 {
		t.Errorf("MaxOutputTokens = %v, want 100", o.MaxOutputTokens)
	}
	if o.Temperature == nil || *o.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", o.Temperature)
	}
	if o.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q, want low", o.ReasoningEffort)
	}
}

// A thinking budget is carried only as the effort it falls in, so the budget itself stays in the
// request's Origin, where a translation reports it.
func TestThinkingBudgetStaysInOrigin(t *testing.T) {
	req := mustParse(t, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":513}}}`)
	if req.Options.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q, want low", req.Options.ReasoningEffort)
	}
	sameJSON(t, "Origin.Raw", req.Origin.Raw, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":513}}}`)
}

// Function calls go to the client in the order they started, whichever finished first.
func TestStreamWriterKeepsCallStartOrder(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: llm.FormatOpenAIResponses}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "a", Name: "first"}},
		{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "b", Name: "second"}},
		{Type: llm.EventItemEnd, Index: 1},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}},
	}
	got := dataLines(t, writeStream(t, events))
	if len(got) != 1 {
		t.Fatalf("chunks = %q, want one", got)
	}
	sameJSON(t, "chunk", []byte(got[0]), `{"candidates":[{"content":{"role":"model","parts":[`+
		`{"functionCall":{"id":"a","name":"first","args":{}}},{"functionCall":{"id":"b","name":"second","args":{}}}]},`+
		`"finishReason":"STOP","index":0}]}`)
}

// ProtoJSON integers: exponents and zero fractions read as whole numbers; fractions, values out of
// range and non-numbers are refused, not rounded.
func TestProtoJSONIntegers(t *testing.T) {
	for _, v := range []string{`"2e0"`, `"2.0"`, `2`} {
		req := mustParse(t, `{"generationConfig":{"maxOutputTokens":`+v+`},"tools":[{"functionDeclarations":[{"name":"p",`+
			`"parameters":{"type":"ARRAY","maxItems":`+v+`,"items":{"type":"STRING"}}}]}]}`)
		if o := req.Options.MaxOutputTokens; o == nil || *o != 2 {
			t.Errorf("maxOutputTokens %s = %v, want 2", v, o)
		}
		sameJSON(t, "parameters for maxItems "+v, req.Tools[0].Parameters, `{"type":"array","maxItems":2,"items":{"type":"string"}}`)
	}
	for _, v := range []string{`1.5`, `1e100`, `"null"`, `"abc"`, `2147483648`} {
		_, err := ParseRequest("m", false, []byte(`{"generationConfig":{"maxOutputTokens":`+v+`}}`))
		if err == nil {
			t.Errorf("maxOutputTokens %s accepted, want invalid_request", v)
			continue
		}
		wantInvalidRequest(t, err)
	}
}

// A Chat upstream may send a call's thought signature in a later delta than the call's first one;
// it still goes back on the call part.
func TestLateChatSignatureReachesTheCall(t *testing.T) {
	d := openaichat.NewStreamDecoder()
	var events []llm.Event
	for _, c := range []string{
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"sig"}}}]}}]}`,
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	} {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		events = append(events, evs...)
	}
	got := dataLines(t, writeStream(t, events))
	sameJSON(t, "last chunk", []byte(got[len(got)-1]), `{"candidates":[{"content":{"role":"model","parts":[`+
		`{"functionCall":{"id":"c1","name":"f","args":{}},"thoughtSignature":"sig"}]},"finishReason":"STOP","index":0}],`+
		`"modelVersion":"m","responseId":"x"}`)
	agg := NewAggregator()
	for _, ev := range events {
		agg.Add(ev)
	}
	body, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"thoughtSignature":"sig"`) {
		t.Errorf("aggregated reply %s has no thoughtSignature", body)
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
	w := NewStreamWriter(&out)
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

// A Devin reasoning signature is written in a thought part of its own in a streamed reply, as
// today's core writes it, and left out of an unstreamed one, which reports it as dropped.
func TestDevinReasoningSignature(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "id1", Model: "m"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "think"},
		{Type: llm.EventItemEnd, Index: 0, Opaque: &llm.Opaque{Format: llm.OpaqueDevin, Data: "sealed"}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	got := dataLines(t, writeStream(t, events))
	if len(got) != 3 {
		t.Fatalf("%d chunks, want 3 (the thought, its signature, the last): %q", len(got), got)
	}
	sameJSON(t, "signature chunk", []byte(got[1]), `{"candidates":[{"content":{"role":"model","parts":[`+
		`{"text":"","thought":true,"thoughtSignature":"sealed"}]},"index":0}],"modelVersion":"m","responseId":"id1"}`)

	a := NewAggregator()
	for _, ev := range events {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sealed") {
		t.Errorf("the unstreamed reply carries the signature: %s", body)
	}
	if l := a.Losses(); len(l) != 1 || l[0].Path != "content.thoughtSignature" {
		t.Errorf("losses = %+v, want the signature reported as dropped", l)
	}
}
