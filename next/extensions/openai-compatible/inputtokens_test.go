package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestInputTokensRefusesWiresWithoutResponses(t *testing.T) {
	for _, wire := range []string{wireChat, wireMessages, wireGemini} {
		t.Run(wire, func(t *testing.T) {
			srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				t.Errorf("the upstream got %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			})
			settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"` + wire + `"}`)
			call := compactCall(settings, "sk-test", "gpt-test", `{"model":"gpt-test","input":"hello"}`)
			call.Request.Native.Format = llm.FormatOpenAIResponsesInputTokens

			err := inputTokens(context.Background(), call, nil)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
				t.Fatalf("err = %+v; want retryable CodeUnsupported on ScopeCredential", err)
			}
			if want := formatNames[wires[wire].format]; !strings.Contains(e.Message, want) {
				t.Errorf("error message = %q, want it to name %q", e.Message, want)
			}
			if got := recorded(); len(got) != 0 {
				t.Errorf("upstream got %d requests, want 0", len(got))
			}
		})
	}
}

func TestInputTokensResponsesPassthrough(t *testing.T) {
	const (
		apiKey = "made-up-input-tokens-key"
		body   = `{"model":"gpt-test","instructions":"Be brief.","input":"hello"}`
		reply  = `{"object":"response.input_tokens","input_tokens":9}`
	)
	srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, reply)
	})
	settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)
	call := compactCall(settings, apiKey, "gpt-test", body)
	call.Request.Native.Format = llm.FormatOpenAIResponsesInputTokens

	var rec compactRecorder
	if err := inputTokens(context.Background(), call, &rec); err != nil {
		t.Fatalf("inputTokens: %v", err)
	}
	if rec.format != llm.FormatOpenAIResponsesInputTokens || rec.contentType != "application/json; charset=utf-8" || rec.finish == nil || rec.finish.Reason != llm.FinishStop {
		t.Fatalf("rec = %+v; want input-token format, the upstream content type, and stop", rec)
	}
	if got := rec.body.String(); got != reply {
		t.Errorf("reply body = %s, want %s", got, reply)
	}
	reqs := recorded()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != "/v1/responses/input_tokens" {
		t.Fatalf("upstream requests = %+v, want 1 POST /v1/responses/input_tokens", reqs)
	}
	if got := reqs[0].header.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+apiKey)
	}
	if got := string(reqs[0].body); got != body {
		t.Errorf("upstream body = %s, want unchanged %s", got, body)
	}
}

func TestInputTokensNamesTheRoutedModel(t *testing.T) {
	srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":3}`)
	})
	settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)
	call := compactCall(settings, "sk-test", "upstream-model", `{"model":"client-alias","input":"hello"}`)
	call.Request.Native.Format = llm.FormatOpenAIResponsesInputTokens

	var rec compactRecorder
	if err := inputTokens(context.Background(), call, &rec); err != nil {
		t.Fatalf("inputTokens: %v", err)
	}
	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	var got map[string]any
	if err := json.Unmarshal(reqs[0].body, &got); err != nil {
		t.Fatalf("upstream body %s: %v", reqs[0].body, err)
	}
	if got["model"] != "upstream-model" || got["input"] != "hello" {
		t.Errorf("upstream body = %s, want model upstream-model and the input unchanged", reqs[0].body)
	}
}
