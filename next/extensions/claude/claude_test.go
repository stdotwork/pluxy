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
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// A translated request carries the effort as thinking, and the effort is no longer reported as
// dropped.
func TestBuildBodyTranslatesEffort(t *testing.T) {
	req := llm.Request{
		Model:   "claude-opus-4-6",
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}},
		Options: llm.Options{ReasoningEffort: "medium"},
		Native:  &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)},
	}
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Passthrough || tr.From != llm.FormatOpenAIChat || len(tr.Losses) != 0 {
		t.Errorf("translation = %+v, want a translation from Chat with no losses", tr)
	}
	if !strings.Contains(string(body), `"output_config":{"effort":"medium"}`) {
		t.Errorf("body %s has no effort", body)
	}
}

// A Messages request goes upstream byte for byte, thinking signatures and cache markers included.
func TestBuildBodyPassesMessagesThrough(t *testing.T) {
	in := `{"model":"claude-test","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"Esig"}]},{"role":"user","content":[{"type":"text","text":"go on","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
	body, tr, err := buildBody(llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != in || !tr.Passthrough {
		t.Errorf("passthrough %v, body %s; want the client's body unchanged", tr.Passthrough, body)
	}
}

func response(status int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func TestUpstreamError(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute).Unix()
	for _, c := range []struct {
		name      string
		resp      *http.Response
		code      string
		scope     string
		retryable bool
		minWaitMS int64
	}{
		{"overloaded", response(529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), llm.CodeUnavailable, llm.ScopeEndpoint, true, 0},
		{"rate limit", response(429, map[string]string{"Retry-After": "20"}, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`), llm.CodeRateLimit, llm.ScopeModel, true, 20000},
		{"subscription window used up", response(429, map[string]string{
			"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": itoa(int(reset)),
		}, `{"type":"error","error":{"type":"rate_limit_error","message":"limit reached"}}`), llm.CodeQuota, llm.ScopeCredential, true, 80 * 60 * 1000},
		{"bad key", response(401, nil, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`), llm.CodeAuth, llm.ScopeCredential, true, 0},
		{"unknown model", response(404, nil, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`), llm.CodeNotFound, llm.ScopeModel, true, 0},
		{"server error", response(500, nil, `{"type":"error","error":{"type":"api_error","message":"boom"}}`), llm.CodeUpstream, llm.ScopeEndpoint, false, 0},
		{"bad request", response(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`), llm.CodeInvalidRequest, llm.ScopeRequest, false, 0},
	} {
		e := upstreamError(c.resp, "claude-nope")
		if e.Code != c.code || e.Scope != c.scope || e.Retryable != c.retryable || e.RetryAfterMS < c.minWaitMS || e.ProviderCode == "" {
			t.Errorf("%s: got %+v; want code %s, scope %s, retryable %v, wait at least %d ms", c.name, e, c.code, c.scope, c.retryable, c.minWaitMS)
		}
	}
}

func TestReadCallback(t *testing.T) {
	for _, c := range []struct{ in, code, state, failure string }{
		{"http://localhost:54545/callback?code=abc&state=xyz", "abc", "xyz", ""},
		{"  abc#xyz \n", "abc", "xyz", ""},
		{"http://localhost:54545/callback?code=abc%23xyz", "abc", "xyz", ""},
		{"http://localhost:54545/callback?error=access_denied&state=xyz", "", "", "access_denied"},
	} {
		code, state, failure := readCallback(c.in)
		if code != c.code || state != c.state || failure != c.failure {
			t.Errorf("readCallback(%q) = %q, %q, %q; want %q, %q, %q", c.in, code, state, failure, c.code, c.state, c.failure)
		}
	}
}

func TestWithBeta(t *testing.T) {
	for in, want := range map[string]string{
		"":               oauthBeta,
		"a, b":           "a,b," + oauthBeta,
		"a," + oauthBeta: "a," + oauthBeta,
	} {
		if got := withBeta(in, oauthBeta); got != want {
			t.Errorf("withBeta(%q) = %q, want %q", in, got, want)
		}
	}
}

// Core redacts a login's input as the user entered it; the extension sends the key trimmed, so it
// removes the trimmed key from an upstream error itself.
func TestAPIKeyLoginErrorHidesTrimmedKey(t *testing.T) {
	const key = "sk-ant-api-echoed"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad key `+r.Header.Get("X-Api-Key")+`"}}`)
	}))
	defer srv.Close()
	_, err := apiKeyLogin(context.Background(), settings{BaseURL: srv.URL}, "  "+key+"\n")
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *llm.Error", err)
	}
	if strings.Contains(e.Message, key) || strings.Contains(string(e.Raw), key) {
		t.Errorf("error shows the key: message %q, raw %s", e.Message, e.Raw)
	}
}

// A translated request gets cache breakpoints unless it already has cache markers; a tool
// property that happens to be named cache_control is not one.
func TestCacheBreakpoints(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"system and last message",
			`{"system":[{"type":"text","text":"S"}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"assistant","content":[{"type":"text","text":"b"},{"type":"thinking","thinking":"t","signature":"E"}]}]}`,
			`{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"assistant","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"thinking","thinking":"t","signature":"E"}]}]}`},
		{"last tool without system, property named cache_control",
			`{"tools":[{"name":"f","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			`{"tools":[{"name":"f","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`},
		{"already marked",
			`{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			`{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`},
	} {
		out, err := addCacheBreakpoints([]byte(c.in))
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(out, json.RawMessage(c.want)) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, out, c.want)
		}
	}
}
