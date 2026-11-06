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
