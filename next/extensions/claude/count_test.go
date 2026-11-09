package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Only Anthropic's own origin counts as its API: https, the host api.anthropic.com, port 443 or
// none, no user info. The cases are the ones today's core's gate is tested with, and more.
func TestIsAnthropicBase(t *testing.T) {
	for base, want := range map[string]bool{
		"https://api.anthropic.com":              true,
		"https://api.anthropic.com/":             true,
		"https://api.anthropic.com:443":          true,
		"https://API.Anthropic.com":              true,
		"HTTPS://api.anthropic.com":              true,
		" https://api.anthropic.com ":            true,
		"https://api.anthropic.com/v1":           true,
		"http://api.anthropic.com":               false,
		"https://api.anthropic.com:8443":         false,
		"https://api.anthropic.com.example":      false,
		"https://api.anthropic.com.evil.example": false,
		"https://evil.example/api.anthropic.com": false,
		"https://user@api.anthropic.com":         false,
		"https://user:pw@api.anthropic.com":      false,
		"https://example.com":                    false,
		"http://127.0.0.1:8080":                  false,
		"https://api.anthropic.com:bad":          false,
		"":                                       false,
		"not a url":                              false,
	} {
		if got := isAnthropicBase(base); got != want {
			t.Errorf("isAnthropicBase(%q) = %v, want %v", base, got, want)
		}
	}
}

func countCall(settings []byte) sdk.Call {
	return sdk.Call{
		Account: protocol.Account{ID: "a", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-ant-api-test"}},
		Request: llm.Request{Model: "claude-test", Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}}},
	}
}

// An account on another base URL answers that it cannot count, so that core tries another
// account and then estimates the count itself, and sends the upstream nothing.
func TestCountTokensOnAnotherBaseURL(t *testing.T) {
	srv := claudetest.New(t)
	err := countTokens(context.Background(), countCall(srv.Settings("")), nil)
	e := llmError(t, err)
	if e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
		t.Errorf("error = %+v, want unsupported, scope credential, retryable", e)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An account on Anthropic's own origin (the default base_url) counts with the upstream: the
// request goes to its count_tokens endpoint, and the upstream's refusal is the error.
func TestCountTokensOnAnthropicsOrigin(t *testing.T) {
	var got []string
	old := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Method+" "+r.URL.String())
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"bad count"}}`)), Request: r}, nil
	})
	t.Cleanup(func() { client.Transport = old })
	for _, settings := range []string{``, `{"base_url":"https://api.anthropic.com"}`, `{"base_url":"https://api.anthropic.com:443/"}`} {
		got = nil
		err := countTokens(context.Background(), countCall([]byte(settings)), nil)
		e := llmError(t, err)
		if e.Code != llm.CodeInvalidRequest || !strings.Contains(e.Message, "bad count") {
			t.Errorf("settings %q: error = %+v, want the upstream's refusal", settings, e)
		}
		if len(got) != 1 || !strings.HasSuffix(got[0], "/v1/messages/count_tokens?beta=true") || !strings.HasPrefix(got[0], "POST https://api.anthropic.com") {
			t.Errorf("settings %q: requests = %v, want one POST to Anthropic's count_tokens", settings, got)
		}
	}
}
