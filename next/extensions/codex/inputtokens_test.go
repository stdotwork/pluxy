package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type tokenCountRecorder struct {
	format      string
	contentType string
	body        strings.Builder
	finish      *llm.Finish
}

func (r *tokenCountRecorder) StartBody(format, contentType string) error {
	r.format, r.contentType = format, contentType
	return nil
}

func (r *tokenCountRecorder) Body(chunk string) error {
	r.body.WriteString(chunk)
	return nil
}

func (r *tokenCountRecorder) Finish(f llm.Finish, _ *llm.Usage) error {
	r.finish = &f
	return nil
}

func inputTokenCall(settings map[string]any, cred *protocol.Credential, body string) sdk.Call {
	raw, _ := json.Marshal(settings)
	return sdk.Call{
		Account: protocol.Account{ID: "codex", Settings: raw, Credential: cred},
		Request: llm.Request{
			Model: "gpt-test",
			Native: &llm.Native{
				Format:  llm.FormatOpenAIResponsesInputTokens,
				Body:    json.RawMessage(body),
				Headers: map[string]string{"session-id": "s1", "originator": "codex_cli_rs"},
			},
		},
	}
}

func TestInputTokensAPIPassthrough(t *testing.T) {
	const (
		apiKey = "made-up-codex-input-token-key"
		body   = `{"model":"gpt-test","instructions":"Be brief.","input":"hello"}`
		reply  = `{"object":"response.input_tokens","input_tokens":11}`
	)
	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	call := inputTokenCall(map[string]any{"base_url": srv.URL + "/v1"}, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: apiKey}, body)

	var rec tokenCountRecorder
	if err := inputTokens(context.Background(), call, &rec); err != nil {
		t.Fatalf("inputTokens: %v", err)
	}
	if rec.format != llm.FormatOpenAIResponsesInputTokens || rec.contentType != "application/json; charset=utf-8" || rec.finish == nil || rec.finish.Reason != llm.FinishStop {
		t.Fatalf("rec = %+v; want input-token format, the upstream content type, and stop", rec)
	}
	if got := rec.body.String(); got != reply {
		t.Errorf("reply body = %s, want %s", got, reply)
	}
	if gotPath != "/v1/responses/input_tokens" || gotAuth != "Bearer "+apiKey || string(gotBody) != body {
		t.Errorf("upstream got path %q, Authorization %q, body %s", gotPath, gotAuth, gotBody)
	}
}

func TestInputTokensRefusesSubscription(t *testing.T) {
	call := inputTokenCall(map[string]any{"base_url": "https://example.invalid/v1"}, subscription("tok-sub", ""), `{"model":"gpt-test","input":"hello"}`)
	err := inputTokens(context.Background(), call, nil)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
		t.Fatalf("err = %+v; want retryable CodeUnsupported on ScopeCredential", err)
	}
}

func TestInputTokensNamesTheRoutedModel(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"object":"response.input_tokens","input_tokens":3}`)
	}))
	defer srv.Close()
	call := inputTokenCall(map[string]any{"base_url": srv.URL + "/v1"}, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "made-up-key"}, `{"model":"client-alias","input":"hello"}`)

	var rec tokenCountRecorder
	if err := inputTokens(context.Background(), call, &rec); err != nil {
		t.Fatalf("inputTokens: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("upstream body %s: %v", gotBody, err)
	}
	if got["model"] != "gpt-test" || got["input"] != "hello" {
		t.Errorf("upstream body = %s, want model gpt-test and the input unchanged", gotBody)
	}
}
