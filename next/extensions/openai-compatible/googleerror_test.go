package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

func failWith(err error) sdk.Handler {
	return func(context.Context, sdk.Call, *sdk.Stream) error { return err }
}

const googleQuotaError = `{"error":{"code":429,"message":"Quota exceeded for project 738291456021.","status":"RESOURCE_EXHAUSTED","details":[` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"PerDay","subject":"project:acme-prod-4821","quotaValue":"15"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`

// An account can point at Google's own host. A client's error from it is in Google's shape, an
// object or the array Google's OpenAI-compatible API wraps it in, and loses what names the owner's
// project, with its classification unchanged, wherever the handler returns it from.
func TestMaskGoogleMasksGoogleShapedErrors(t *testing.T) {
	inner := strings.TrimSuffix(strings.TrimPrefix(googleQuotaError, `{"error":`), `}`)
	for name, body := range map[string]string{"object": googleQuotaError, "array": "[" + googleQuotaError + "]", "error object alone, as a stream keeps it": inner} {
		t.Run(name, func(t *testing.T) {
			e := &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, Status: 429, Retryable: true, RetryAfterMS: 7000,
				Message: "Quota exceeded for project 738291456021.", Raw: json.RawMessage(body)}
			got := maskGoogle(failWith(e))(context.Background(), sdk.Call{}, nil)
			var le *llm.Error
			if !errors.As(got, &le) {
				t.Fatalf("got %v, want an *llm.Error", got)
			}
			if strings.Contains(le.Message, "738291456021") || strings.Contains(string(le.Raw), "738291456021") || strings.Contains(string(le.Raw), "acme-prod-4821") {
				t.Errorf("project named: %q %s", le.Message, le.Raw)
			}
			for _, keep := range []string{`"retryDelay":"34s"`, `"quotaId":"PerDay"`, `"quotaValue":"15"`, `"status":"RESOURCE_EXHAUSTED"`} {
				if !strings.Contains(string(le.Raw), keep) {
					t.Errorf("raw lost %s: %s", keep, le.Raw)
				}
			}
			if le.Code != e.Code || le.Scope != e.Scope || !le.Retryable || le.RetryAfterMS != 7000 || le.Status != 429 {
				t.Errorf("classification changed: %+v", le)
			}
		})
	}
}

// Another vendor's error that merely mentions a project is not Google's and is left alone, and so
// is an error that is not an *llm.Error.
func TestMaskGoogleLeavesOtherErrorsAlone(t *testing.T) {
	const body = `{"error":{"message":"Project 'proj_abc' is over its budget","type":"insufficient_quota","code":"insufficient_quota"}}`
	e := &llm.Error{Code: llm.CodeQuota, Message: "Project 'proj_abc' is over its budget", Raw: json.RawMessage(body)}
	if got := maskGoogle(failWith(e))(context.Background(), sdk.Call{}, nil); got != e {
		t.Errorf("got %+v, want the error itself", got)
	}
	plain := errors.New("boom")
	if got := maskGoogle(failWith(plain))(context.Background(), sdk.Call{}, nil); got != plain {
		t.Errorf("got %v", got)
	}
	if got := maskGoogle(failWith(nil))(context.Background(), sdk.Call{}, nil); got != nil {
		t.Errorf("got %v", got)
	}
}

// The owner's own calls build their errors with upstreamError and are not wrapped: they keep the
// project, which is how the owner finds the API to enable.
func TestUpstreamErrorKeepsTheProject(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(googleQuotaError))}
	e := upstreamError(resp, "")
	if !strings.Contains(e.Message, "738291456021") || !strings.Contains(string(e.Raw), "acme-prod-4821") {
		t.Errorf("the project is gone: %q %s", e.Message, e.Raw)
	}
}
