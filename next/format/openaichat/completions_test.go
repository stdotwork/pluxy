package openaichat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func jsonEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A completions request is carried as the Chat request today's core makes of it.
func TestParseCompletionsRequest(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"model":"m","prompt":"Say hello"}`, `{"model":"m","messages":[{"role":"user","content":"Say hello"}]}`},
		{`{"model":"m","prompt":["only one"]}`, `{"model":"m","messages":[{"role":"user","content":"only one"}]}`},
		{`{"model":"m","prompt":""}`, `{"model":"m","messages":[{"role":"user","content":"Complete this:"}]}`},
		{`{"model":"m"}`, `{"model":"m","messages":[{"role":"user","content":"Complete this:"}]}`},
		{`{"model":"m","prompt":"p","max_tokens":16.0,"temperature":1,"top_p":0.5,"frequency_penalty":0.1,"presence_penalty":0.2,
		  "stop":["\n"],"stream":true,"logprobs":2,"top_logprobs":3,"echo":true,"n":2,"suffix":"s","best_of":3,"user":"u"}`,
			`{"model":"m","messages":[{"role":"user","content":"p"}],"max_tokens":16,"temperature":1,"top_p":0.5,"frequency_penalty":0.1,
			  "presence_penalty":0.2,"stop":["\n"],"stream":true,"logprobs":true,"top_logprobs":3,"echo":true}`},
	} {
		req, err := ParseCompletionsRequest([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if req.Native == nil || req.Native.Format != llm.FormatOpenAIChat {
			t.Fatalf("%s: native = %+v", c.in, req.Native)
		}
		jsonEqual(t, req.Native.Body, c.want)
	}
	for _, in := range []string{`{"model":"m","prompt":["a","b"]}`, `{"model":"m","prompt":[1,2]}`, `[1]`, `{`} {
		if _, err := ParseCompletionsRequest([]byte(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func completionEvents() []llm.Event {
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "chatcmpl-1", Model: "m", Created: 7}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "Hel"},
		{Type: llm.EventTextDelta, Index: 0, Delta: "lo"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Usage: &llm.Usage{InputTokens: llm.Int64(4), OutputTokens: llm.Int64(2)}},
	}
}

func TestCompletionsAggregator(t *testing.T) {
	a := NewCompletionsAggregator()
	for _, ev := range completionEvents() {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, out, `{"id":"chatcmpl-1","object":"text_completion","created":7,"model":"m",
		"choices":[{"index":0,"text":"Hello","finish_reason":"stop"}],
		"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
}

// The role chunk, which has no text, is left out; text, finish and usage chunks are converted;
// [DONE] ends the stream. An error after the start goes as the Chat stream writes it.
func TestCompletionsStreamWriter(t *testing.T) {
	var out strings.Builder
	w := NewCompletionsStreamWriter(&out, true)
	for _, ev := range completionEvents() {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	var datas []string
	for _, line := range strings.Split(out.String(), "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			datas = append(datas, d)
		}
	}
	want := []string{
		`{"id":"chatcmpl-1","object":"text_completion","created":7,"model":"m","choices":[{"index":0,"text":"Hel"}]}`,
		`{"id":"chatcmpl-1","object":"text_completion","created":7,"model":"m","choices":[{"index":0,"text":"lo"}]}`,
		`{"id":"chatcmpl-1","object":"text_completion","created":7,"model":"m","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","object":"text_completion","created":7,"model":"m","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	}
	if len(datas) != len(want)+1 || datas[len(datas)-1] != "[DONE]" {
		t.Fatalf("stream:\n%s", out.String())
	}
	for i, w := range want {
		jsonEqual(t, []byte(datas[i]), w)
	}

	out.Reset()
	w = NewCompletionsStreamWriter(&out, false)
	_ = w.Write(completionEvents()[0])
	_ = w.Write(llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUpstream, Message: "broke"}})
	if !strings.HasPrefix(out.String(), `data: {"error":`) || strings.Count(out.String(), "data: ") != 1 {
		t.Errorf("error stream:\n%s", out.String())
	}
}
