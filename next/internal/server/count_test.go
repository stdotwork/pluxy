package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// countHandler returns the client API handler with one account whose count attempts answer with
// count; generate attempts fail the test.
func countHandler(t *testing.T, count func(req llm.Request) []llm.Event) (http.Handler, *[]llm.Request) {
	t.Helper()
	var seen []llm.Request
	gen := func(context.Context, string, protocol.Account, llm.Request) route.Stream {
		t.Error("a count went to generate")
		return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInternal}}}}
	}
	cnt := func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
		seen = append(seen, req)
		return &events{evs: count(req)}
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "work"}}, gen, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}}, Router: router, Attempt: gen, Count: cnt}}), &seen
}

func counted(format string, n int64, raw string) func(llm.Request) []llm.Event {
	return func(req llm.Request) []llm.Event {
		return []llm.Event{
			{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Model: req.Model, Format: format}},
			{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Usage: &llm.Usage{InputTokens: llm.Int64(n), Raw: json.RawMessage(raw)}},
		}
	}
}

func postCount(h http.Handler, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer k")
	h.ServeHTTP(w, r)
	return w
}

// A count from an upstream in the client's format answers with the upstream's reply as it came;
// one from another format answers with the count in the client's shape.
func TestCountTokens(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		count            func(llm.Request) []llm.Event
		want             string
	}{
		{"messages from messages", "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`,
			counted(llm.FormatAnthropicMessages, 42, `{"input_tokens":42,"extra":1}`), `{"input_tokens":42,"extra":1}`},
		{"messages from gemini", "/v1/messages/count_tokens", `{"model":"gemini-x","messages":[{"role":"user","content":"hi"}]}`,
			counted(llm.FormatGeminiGenerate, 7, `{"totalTokens":7}`), `{"input_tokens":7}`},
		{"gemini from gemini", "/v1beta/models/gemini-x:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			counted(llm.FormatGeminiGenerate, 3, `{"totalTokens":3,"promptTokensDetails":[{"modality":"IMAGE","tokenCount":3}]}`),
			`{"totalTokens":3,"promptTokensDetails":[{"modality":"IMAGE","tokenCount":3}]}`},
		{"gemini from messages", "/v1beta/models/claude-x:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			counted(llm.FormatAnthropicMessages, 5, `{"input_tokens":5}`), `{"totalTokens":5,"promptTokensDetails":[{"modality":"TEXT","tokenCount":5}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := countHandler(t, c.count)
			w := postCount(h, c.path, c.body)
			if w.Code != http.StatusOK || w.Header().Get(TraceHeader) == "" {
				t.Fatalf("answered %d (trace %q): %s", w.Code, w.Header().Get(TraceHeader), w.Body)
			}
			if mustCompact(w.Body.String()) != mustCompact(c.want) {
				t.Errorf("body = %s, want %s", w.Body, c.want)
			}
		})
	}
}

func mustCompact(s string) string {
	var v any
	_ = json.Unmarshal([]byte(s), &v)
	b, _ := json.Marshal(v)
	return string(b)
}

// A Gemini count of a whole GenerateContent request (generateContentRequest) is read from that
// request, and the client's body goes on as it came; the model's thinking suffix is taken off.
func TestCountTokensRequest(t *testing.T) {
	h, seen := countHandler(t, counted(llm.FormatGeminiGenerate, 1, `{"totalTokens":1}`))
	body := `{"generateContentRequest":{"model":"models/gemini-x","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`
	if w := postCount(h, "/v1beta/models/gemini-x(high):countTokens", body); w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body)
	}
	req := (*seen)[0]
	if req.Model != "gemini-x" || req.Options.ThinkingSuffix != "high" || req.Options.ReasoningEffort != "high" || len(req.Items) != 1 || string(req.Native.Body) != body || req.Stream {
		t.Errorf("request = %+v, native %s", req, req.Native.Body)
	}
}

// When no account can count, the client gets the error; an extension's count reply without a
// count is a protocol error.
func TestCountTokensFails(t *testing.T) {
	h, _ := countHandler(t, func(llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "extension x does not count tokens"}}}
	})
	w := postCount(h, "/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code < 400 || !strings.Contains(w.Body.String(), "does not count tokens") || !strings.Contains(w.Body.String(), `"type":"error"`) {
		t.Errorf("answered %d: %s", w.Code, w.Body)
	}
	h, _ = countHandler(t, func(req llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}}, {Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}}
	})
	w = postCount(h, "/v1beta/models/m:countTokens", `{"contents":[]}`)
	if w.Code < 500 || !strings.Contains(w.Body.String(), "no input_tokens") {
		t.Errorf("answered %d: %s", w.Code, w.Body)
	}
}
