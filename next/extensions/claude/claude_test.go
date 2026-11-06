package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestApplyEffort(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		maxTokens     int
		want          string // thinking and output_config as JSON, "" when not applied
	}{
		{"claude-opus-4-6", "high", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`},
		{"claude-sonnet-5-5-20261001", "xhigh", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`},
		{"claude-fable-5-1", "minimal", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`},
		{"claude-opus-4-6", "auto", 32000, `{"thinking":{"type":"adaptive"}}`},
		{"claude-sonnet-4-5", "medium", 32000, `{"thinking":{"type":"enabled","budget_tokens":8192}}`},
		{"claude-test", "high", 32000, ""},
		{"claude-opus-4-1", "high", 32000, `{"thinking":{"type":"enabled","budget_tokens":24576}}`},
		{"claude-3-7-sonnet-latest", "low", 32000, `{"thinking":{"type":"enabled","budget_tokens":1024}}`},
		{"claude-sonnet-4", "max", 32000, `{"thinking":{"type":"enabled","budget_tokens":31999}}`},
		{"claude-sonnet-4", "auto", 32000, `{"thinking":{"type":"enabled","budget_tokens":1024}}`},
		{"claude-sonnet-4", "none", 32000, `{"thinking":{"type":"disabled"}}`},
		{"claude-sonnet-4", "low", 1024, ""},
		{"claude-opus-4-6", "unheard-of", 32000, ""},
		{"claude-3-5-sonnet-latest", "high", 32000, ""},
		{"gpt-5", "high", 32000, ""},
	} {
		body := []byte(`{"model":"` + c.model + `","max_tokens":` + itoa(c.maxTokens) + `}`)
		out, applied, err := applyEffort(body, c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if applied || string(out) != string(body) {
				t.Errorf("%s %s: applied %v, body %s; want left alone", c.model, c.effort, applied, out)
			}
			continue
		}
		var got, want map[string]json.RawMessage
		_ = json.Unmarshal(out, &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if !applied || len(got) != len(want)+2 {
			t.Errorf("%s %s: applied %v, body %s; want %s", c.model, c.effort, applied, out, c.want)
			continue
		}
		for k, v := range want {
			if !jsonEqual(got[k], v) {
				t.Errorf("%s %s: %s = %s, want %s", c.model, c.effort, k, got[k], v)
			}
		}
	}
}

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
		{"rate limit", response(429, map[string]string{"Retry-After": "20"}, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`), llm.CodeRateLimit, llm.ScopeCredential, true, 20000},
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
