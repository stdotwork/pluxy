package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestServiceUnavailableIsNotRetrySafe(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable",
		Header: http.Header{"Retry-After": {"7"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))}
	e := upstreamError(resp, "m")
	if e.Retryable {
		t.Errorf("503 marked retryable: %+v", e)
	}
	if e.Code != llm.CodeUnavailable || e.Scope != llm.ScopeEndpoint {
		t.Errorf("code %q scope %q, want %q %q", e.Code, e.Scope, llm.CodeUnavailable, llm.ScopeEndpoint)
	}
	if e.RetryAfterMS != 7000 {
		t.Errorf("retry_after_ms = %d, want 7000", e.RetryAfterMS)
	}
}

func TestTooManyRequestsBlocksTheModel(t *testing.T) {
	for _, c := range []struct{ body, code string }{
		{`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`, llm.CodeRateLimit},
		{`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`, llm.CodeQuota},
	} {
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader(c.body))}
		e := upstreamError(resp, "m")
		if e.Code != c.code || e.Scope != llm.ScopeModel || !e.Retryable {
			t.Errorf("%s: code %q scope %q retryable %v, want %q on the model, retryable", c.body, e.Code, e.Scope, e.Retryable, c.code)
		}
	}
}

func TestTooManyRequestsWithoutModelIsTheAccounts(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Rate limit reached"}}`))}
	if e := upstreamError(resp, ""); e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeCredential {
		t.Errorf("code %q scope %q, want a rate limit on the credential", e.Code, e.Scope)
	}
}
