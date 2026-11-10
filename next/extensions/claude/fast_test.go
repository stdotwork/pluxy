package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestIsFast(t *testing.T) {
	for _, c := range []struct {
		body, betas string
		want        bool
	}{
		{`{"speed":"fast"}`, "", true},
		{`{"speed":" FAST "}`, "", true},
		{`{"speed":"standard"}`, "", false},
		{`{"speed":true}`, "", false},
		{`{}`, "a, fast-mode-2026-02-01", true},
		{`{}`, "fast-mode-2026-02-02", false},
		{`not json`, "", false},
	} {
		if got := isFast([]byte(c.body), c.betas); got != c.want {
			t.Errorf("isFast(%s, %q) = %v, want %v", c.body, c.betas, got, c.want)
		}
	}
}

// The bodies are today's core's (claude_executor_beta_policy_test.go): Anthropic's and Claude
// Code's wording of the refusal, and two real rate limits.
func TestFastCredits429(t *testing.T) {
	for _, c := range []struct {
		body    string
		headers map[string]string
		cooling bool
		scope   string
	}{
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`, nil, false, llm.ScopeRequest},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Fast mode requires usage credits"}}`, nil, false, llm.ScopeRequest},
		{`Fast request rejected`, nil, false, llm.ScopeRequest},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit."}}`, nil, false, llm.ScopeModel},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"This organization has exceeded its usage limit."}}`, nil, false, llm.ScopeModel},
		// A refused shared window is the account's, whatever the message; with model-level
		// cooling, today's core checks for the credits refusal first.
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`,
			map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"}, false, llm.ScopeCredential},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`,
			map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"}, true, llm.ScopeRequest},
	} {
		if e := upstreamError(response(429, c.headers, c.body), "claude-x", c.cooling); e.Scope != c.scope || e.Status != 429 {
			t.Errorf("%s (cooling %v): got %+v, want scope %s", c.body, c.cooling, e, c.scope)
		}
	}
	// Only a 429 carries the refusal.
	if e := upstreamError(response(500, nil, `{"error":{"message":"Usage credits are required for fast mode."}}`), "claude-x", false); e.Scope != llm.ScopeEndpoint {
		t.Errorf("500: got %+v, want scope endpoint", e)
	}
}

// A fast request's failure on Anthropic's API is the request's, except a refused shared window.
func TestFastFailure(t *testing.T) {
	for _, e := range []*llm.Error{
		{Code: llm.CodeAuth, Scope: llm.ScopeCredential},
		{Code: llm.CodeRateLimit, Scope: llm.ScopeModel},
		{Code: llm.CodeUnavailable, Scope: llm.ScopeEndpoint},
	} {
		if got := fastFailure(e); got.Scope != llm.ScopeRequest {
			t.Errorf("%s: scope %s, want request", e.Code, got.Scope)
		}
	}
	if got := fastFailure(&llm.Error{Code: llm.CodeQuota, Scope: llm.ScopeCredential}); got.Scope != llm.ScopeCredential {
		t.Errorf("quota: scope %s, want credential", got.Scope)
	}
}

// post decides a request is fast from its anthropic-beta header and from the betas list of its
// body, which bodyBetas takes out of the body, as well as from its speed. Each fast request that
// fails on Anthropic's own API fails as the request's; the fast-mode beta goes upstream once.
func TestPostSeesFastModeBeforeBetasAreStripped(t *testing.T) {
	var sent http.Header
	old := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = r.Header.Clone()
		return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"boom"}}`)), Request: r}, nil
	})
	t.Cleanup(func() { client.Transport = old })
	s, err := readSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, body, header string
		fast               bool
	}{
		{"speed in the body", `{"speed":"fast","messages":[]}`, "", true},
		{"betas list in the body", `{"betas":["fast-mode-2026-02-01"],"messages":[]}`, "", true},
		{"betas list with other betas", `{"betas":["b1"," fast-mode-2026-02-01 "],"messages":[]}`, "", true},
		{"anthropic-beta header", `{"messages":[]}`, "fast-mode-2026-02-01", true},
		{"both", `{"betas":["fast-mode-2026-02-01"],"messages":[]}`, "fast-mode-2026-02-01", true},
		{"not fast", `{"betas":["b1"],"messages":[]}`, "b2", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			headers := map[string]string{}
			if c.header != "" {
				headers["anthropic-beta"] = c.header
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-ant-api-test"}},
				Request: llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Headers: headers}},
			}
			_, err := post(context.Background(), call, s, "/v1/messages", []byte(c.body), true, false)
			e := llmError(t, err)
			want := llm.ScopeEndpoint
			if c.fast {
				want = llm.ScopeRequest
			}
			if e.Scope != want {
				t.Errorf("scope = %s, want %s", e.Scope, want)
			}
			n := strings.Count(","+strings.ReplaceAll(sent.Get("Anthropic-Beta"), " ", "")+",", ",fast-mode-2026-02-01,")
			if (n == 1) != c.fast || n > 1 {
				t.Errorf("Anthropic-Beta = %q names the fast-mode beta %d times, want %v", sent.Get("Anthropic-Beta"), n, map[bool]string{true: "once", false: "none"}[c.fast])
			}
		})
	}
}

func TestPriorityTier(t *testing.T) {
	for _, c := range []struct {
		n    *llm.Native
		want bool
	}{
		{&llm.Native{Format: llm.FormatOpenAIResponses, Body: []byte(`{"service_tier":"priority"}`)}, true},
		{&llm.Native{Format: llm.FormatOpenAIResponses, Body: []byte(`{"service_tier":"flex"}`)}, false},
		{&llm.Native{Format: llm.FormatOpenAIChat, Body: []byte(`{"service_tier":"priority"}`)}, false},
		{nil, false},
	} {
		if got := priorityTier(c.n); got != c.want {
			t.Errorf("priorityTier(%+v) = %v, want %v", c.n, got, c.want)
		}
	}
}
