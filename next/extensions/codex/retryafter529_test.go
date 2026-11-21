package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A 529 keeps the upstream's Retry-After, with the code and scope of any other 5xx.
func TestStatus529KeepsRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: 529, Status: "529", Header: http.Header{"Retry-After": {"7"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))}
	e := statusError(resp, "m")
	if e.RetryAfterMS != 7000 || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeEndpoint || e.Retryable {
		t.Errorf("529 = %+v, want an endpoint failure that waits 7000 ms", e)
	}
}
