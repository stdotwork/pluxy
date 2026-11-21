package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func replyOf(status int, header http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

// An account can point at Google's own host. A client's error from it is in Google's shape, an
// object or the array Google's OpenAI-compatible API wraps it in, and loses what names the owner's
// project, with its classification unchanged. The owner's own calls keep it.
func TestUpstreamErrorMasksGoogleProject(t *testing.T) {
	const object = `{"error":{"code":429,"message":"Quota exceeded for project 738291456021.","status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"PerDay","subject":"project:acme-prod-4821","quotaValue":"15"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`
	for name, body := range map[string]string{"object": object, "array": "[" + object + "]"} {
		t.Run(name, func(t *testing.T) {
			e := upstreamError(replyOf(http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}}, body), "gemini-x")
			if strings.Contains(e.Message, "738291456021") || strings.Contains(string(e.Raw), "738291456021") || strings.Contains(string(e.Raw), "acme-prod-4821") {
				t.Errorf("project named: %q %s", e.Message, e.Raw)
			}
			for _, keep := range []string{`"retryDelay":"34s"`, `"quotaId":"PerDay"`, `"quotaValue":"15"`, `"status":"RESOURCE_EXHAUSTED"`} {
				if !strings.Contains(string(e.Raw), keep) {
					t.Errorf("raw lost %s: %s", keep, e.Raw)
				}
			}
			if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.RetryAfterMS != 7000 || e.Status != 429 {
				t.Errorf("classification changed: %+v", e)
			}
			if o := ownerUpstreamError(replyOf(http.StatusTooManyRequests, http.Header{}, body), "gemini-x"); !strings.Contains(string(o.Raw), "acme-prod-4821") {
				t.Errorf("the owner's error lost the project: %s", o.Raw)
			}
		})
	}
}

// Another vendor's error that merely mentions a project is not Google's and is left alone.
func TestUpstreamErrorLeavesOtherVendorsAlone(t *testing.T) {
	const body = `{"error":{"message":"Project 'proj_abc' is over its budget","type":"insufficient_quota","code":"insufficient_quota"}}`
	e := upstreamError(replyOf(http.StatusTooManyRequests, http.Header{}, body), "m")
	if !strings.Contains(e.Message, "proj_abc") || string(e.Raw) != body {
		t.Errorf("changed: %q %s", e.Message, e.Raw)
	}
}
