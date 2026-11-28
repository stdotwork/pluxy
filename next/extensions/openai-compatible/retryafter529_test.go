package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestStatus529KeepsRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: 529, Status: "529", Header: http.Header{"Retry-After": {"7"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))}
	e := upstreamError(resp, "m")
	if e.RetryAfterMS != 7000 || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeEndpoint || e.Retryable {
		t.Errorf("529 = %+v, want an endpoint failure that waits 7000 ms", e)
	}
}
