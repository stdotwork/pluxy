package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A 429 on a request, a rate limit or a used-up quota, holds back only the model on the account,
// as today's core cools only the model that answered.
func TestTooManyRequestsBlocksTheModel(t *testing.T) {
	for _, c := range []struct{ body, code string }{
		{`{"error":{"message":"Rate limit reached","type":"rate_limit_reached_error"}}`, llm.CodeRateLimit},
		{`{"error":{"message":"Your quota is used up","type":"exceeded_current_quota_error"}}`, llm.CodeQuota},
	} {
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader(c.body))}
		e := upstreamError(resp, "kimi-for-coding")
		if e.Code != c.code || e.Scope != llm.ScopeModel || !e.Retryable {
			t.Errorf("%s: code %q scope %q retryable %v, want %q on the model, retryable", c.body, e.Code, e.Scope, e.Retryable, c.code)
		}
	}
}
