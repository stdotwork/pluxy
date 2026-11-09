package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
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
