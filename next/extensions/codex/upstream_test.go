package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A 503 does not show that the upstream never ran the request (a gateway may answer 503 after
// the model started), so another account must not be tried.
func TestServiceUnavailableIsNotRetrySafe(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable",
		Header: http.Header{"Retry-After": {"7"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))}
	e := statusError(resp, "gpt-6.1-sol")
	if e.Retryable {
		t.Errorf("503 marked retryable: %+v", e)
	}
	if e.RetryAfterMS != 7000 {
		t.Errorf("retry_after_ms = %d, want 7000", e.RetryAfterMS)
	}
}

// A usage_limit_reached refusal carries the length of the used-up window from Codex's rate-limit
// headers (of the windows at 100%, the one that resets last), and the window's reset when the
// body names none.
func TestUsageLimitWindow(t *testing.T) {
	refusal := func(h http.Header, body string) *http.Response {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: h,
			Body: io.NopCloser(strings.NewReader(body))}
	}
	h := http.Header{}
	h.Set("X-Codex-Primary-Used-Percent", "100")
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	h.Set("X-Codex-Primary-Reset-After-Seconds", "600")
	h.Set("X-Codex-Secondary-Used-Percent", "100")
	h.Set("X-Codex-Secondary-Window-Minutes", "10080")
	h.Set("X-Codex-Secondary-Reset-After-Seconds", "86400")
	e := statusError(refusal(h, `{"error":{"type":"usage_limit_reached","message":"limit"}}`), "m")
	if e.QuotaWindowMinutes != 10080 {
		t.Errorf("both windows used up: window %d, want the weekly window", e.QuotaWindowMinutes)
	}
	e = statusError(refusal(h, `{"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":60}}`), "m")
	if e.QuotaWindowMinutes != 10080 || e.RetryAfterMS != 60_000 {
		t.Errorf("with resets_in_seconds: window %d, retry after %d ms; want the body's reset", e.QuotaWindowMinutes, e.RetryAfterMS)
	}
	h.Set("X-Codex-Secondary-Used-Percent", "40")
	if e := statusError(refusal(h, `{"error":{"type":"usage_limit_reached"}}`), "m"); e.QuotaWindowMinutes != 300 {
		t.Errorf("only the primary used up: window %d, want 300", e.QuotaWindowMinutes)
	}
	if e := statusError(refusal(http.Header{}, `{"error":{"type":"usage_limit_reached"}}`), "m"); e.QuotaWindowMinutes != 0 || e.RetryAfterMS != 0 {
		t.Errorf("no headers: window %d, retry after %d", e.QuotaWindowMinutes, e.RetryAfterMS)
	}
}

func usageLimitRefusal(h http.Header, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: h,
		Body: io.NopCloser(strings.NewReader(body))}
}

// A usage_limit_reached refusal whose body names no reset waits until the used-up window resets,
// when Codex's headers say when.
func TestUsageLimitWaitsForTheWindowReset(t *testing.T) {
	h := http.Header{}
	h.Set("X-Codex-Primary-Used-Percent", "100")
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	h.Set("X-Codex-Primary-Reset-After-Seconds", "600")
	h.Set("X-Codex-Secondary-Used-Percent", "100")
	h.Set("X-Codex-Secondary-Window-Minutes", "10080")
	h.Set("X-Codex-Secondary-Reset-After-Seconds", "86400")
	e := statusError(usageLimitRefusal(h, `{"error":{"type":"usage_limit_reached","message":"limit"}}`), "m")
	if e.RetryAfterMS < 86_399_000 || e.RetryAfterMS > 86_400_000 {
		t.Errorf("retry after %d ms, want the weekly window's reset (86400 s)", e.RetryAfterMS)
	}
	h = http.Header{}
	h.Set("X-Codex-Primary-Used-Percent", "100")
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	if e := statusError(usageLimitRefusal(h, `{"error":{"type":"usage_limit_reached"}}`), "m"); e.RetryAfterMS != 0 {
		t.Errorf("a window without a reset time gave retry after %d ms, want none", e.RetryAfterMS)
	}
}

// An endpoint call's 404 that does not name a missing model is the endpoint missing upstream,
// scoped to the request so routing blocks no model; one that names the model stays model-scoped.
func TestEndpointNotFoundBlocksNoModel(t *testing.T) {
	notFound := func(body string) *http.Response {
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(body))}
	}
	if e := endpointStatusError(notFound(`{"detail":"Not Found"}`), "gpt-6.1-sol"); e.Scope != "request" || e.Retryable {
		t.Errorf("missing endpoint: %+v", e)
	}
	if e := endpointStatusError(notFound(`{"error":{"code":"model_not_found","message":"no such model"}}`), "gpt-6.1-sol"); e.Scope != "model" || !e.Retryable {
		t.Errorf("missing model: %+v", e)
	}
}

// Which refusals say the turn or the model is full: the codes turn_rate_limit_exceeded,
// model_at_capacity and model_is_at_capacity, and, with no code at all, a message that says "at
// capacity" for a rate_limit_error or for the model. Another explicit code keeps its own handling.
func TestCapacityRefusal(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{"turn code", `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`, true},
		{"turn code alone", `{"error":{"code":"turn_rate_limit_exceeded"}}`, true},
		{"turn code at the top", `{"code":"turn_rate_limit_exceeded"}`, true},
		{"turn code in body.error", `{"body":{"error":{"code":"turn_rate_limit_exceeded"}}}`, true},
		{"turn code in response.error", `{"type":"response.failed","response":{"error":{"code":"turn_rate_limit_exceeded"}}}`, true},
		{"turn code, case and spaces", `{"error":{"code":" Turn_Rate_Limit_Exceeded "}}`, true},
		{"model_at_capacity", `{"error":{"code":"model_at_capacity","message":"busy"}}`, true},
		{"model_is_at_capacity", `{"error":{"code":"model_is_at_capacity","message":"busy"}}`, true},
		{"rate limit type, capacity message, no code", `{"error":{"type":"rate_limit_error","message":"Codex is at capacity, please try again later"}}`, true},
		{"model wording, no code", `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`, true},
		{"model wording at the top", `{"message":"The model is at capacity"}`, true},
		{"model code named in the message", `{"error":{"message":"error: model_is_at_capacity"}}`, true},
		// The first code found decides, in the order error.code, top level, body.error, response.error,
		// as in today's core: a later capacity code does not override an earlier other code.
		{"other code first, capacity code at the top", `{"error":{"code":"rate_limit_exceeded"},"code":"turn_rate_limit_exceeded"}`, false},
		{"other code first, capacity code in body.error", `{"error":{"code":"rate_limit_exceeded"},"body":{"error":{"code":"turn_rate_limit_exceeded"}}}`, false},
		{"capacity code first, other code at the top", `{"error":{"code":"turn_rate_limit_exceeded"},"code":"rate_limit_exceeded"}`, true},
		{"other code at the top hides a later capacity code", `{"code":"rate_limit_exceeded","body":{"error":{"code":"turn_rate_limit_exceeded"}}}`, false},
		{"code only in the message", `{"error":{"type":"server_error","message":"docs mention turn_rate_limit_exceeded"}}`, false},
		{"plain rate limit", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Too many requests"}}`, false},
		{"another code blocks the rate limit message", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Codex is at capacity"}}`, false},
		{"another code blocks the model wording", `{"error":{"code":"rate_limit_exceeded","message":"Selected model is at capacity"}}`, false},
		{"capacity message without the rate limit type", `{"error":{"type":"server_error","message":"Codex is at capacity"}}`, false},
		{"capacity in a field other than the message", `{"error":{"type":"rate_limit_error","param":"Codex is at capacity"}}`, false},
		{"rate limit type without a capacity message", `{"error":{"type":"rate_limit_error","message":"Too many requests"}}`, false},
		{"usage limit", `{"error":{"type":"usage_limit_reached","message":"limit"}}`, false},
		{"error is a string", `{"error":"model is at capacity"}`, false},
		{"not JSON", `model is at capacity`, false},
		{"empty", ``, false},
	} {
		if got := capacityRefusal([]byte(c.body)); got != c.want {
			t.Errorf("%s: capacityRefusal(%s) = %v, want %v", c.name, c.body, got, c.want)
		}
	}
}

func capacityReply(status int, h http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// A capacity refusal is a rate limit on the model, not on the account: it was refused before it
// ran, so another account may take it; the client sees a 429 whatever the upstream's status; the
// upstream's Retry-After is kept. It is not a quota block, and a usage limit stays one.
func TestCapacityRefusalIsAModelRateLimit(t *testing.T) {
	turn := `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity"}}`
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadRequest, http.StatusServiceUnavailable} {
		e := statusError(capacityReply(status, http.Header{"Retry-After": {"20"}}, turn), "m")
		if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.Status != http.StatusTooManyRequests ||
			e.RetryAfterMS != 20_000 || e.ProviderCode != "turn_rate_limit_exceeded" {
			t.Errorf("upstream status %d: %+v; want a retryable rate limit on the model, status 429, retry after 20000 ms", status, e)
		}
	}
	e := statusError(capacityReply(http.StatusBadRequest, nil, `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`), "m")
	if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.Status != http.StatusTooManyRequests || e.RetryAfterMS != 0 {
		t.Errorf("model wording: %+v; want a retryable rate limit on the model, status 429, no retry time", e)
	}
	// A usage limit that also carries the turn code is still the account's quota.
	e = statusError(capacityReply(http.StatusTooManyRequests, nil,
		`{"error":{"type":"usage_limit_reached","code":"turn_rate_limit_exceeded","resets_in_seconds":3600}}`), "m")
	if e.Code != llm.CodeQuota || e.Scope != llm.ScopeCredential || e.RetryAfterMS != 3_600_000 {
		t.Errorf("usage limit with the turn code: %+v; want quota on the credential, retry after 3600000 ms", e)
	}
	// A plain rate limit keeps blocking the account.
	e = statusError(capacityReply(http.StatusTooManyRequests, nil,
		`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Codex is at capacity"}}`), "m")
	if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeCredential {
		t.Errorf("plain rate limit: %+v; want a rate limit on the credential", e)
	}
}

// A capacity refusal reported inside an open stream is a rate limit on the model too; it is not
// marked retryable, as the stream is open.
func TestCapacityRefusalInStream(t *testing.T) {
	raw := `{"type":"response.failed","response":{"status":"failed","error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity"}}}`
	err := classify(&llm.Error{Code: llm.CodeUpstream, Message: "Codex is at capacity", Raw: llm.RawJSON([]byte(raw))})
	e, ok := err.(*llm.Error)
	if !ok || e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || e.Status != http.StatusTooManyRequests || e.Retryable {
		t.Errorf("classify = %+v; want a rate limit on the model, status 429, not retryable", err)
	}
}
