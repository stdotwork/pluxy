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
