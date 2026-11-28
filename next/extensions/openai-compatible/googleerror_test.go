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
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func failWith(err error) sdk.Handler {
	return func(context.Context, sdk.Call, *sdk.Stream) error { return err }
}

const googleQuotaError = `{"error":{"code":429,"message":"Quota exceeded for project 738291456021.","status":"RESOURCE_EXHAUSTED","details":[` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"PerDay","subject":"project:acme-prod-4821","quotaValue":"15"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`

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

func TestUpstreamErrorKeepsTheProject(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(googleQuotaError))}
	e := upstreamError(resp, "")
	if !strings.Contains(e.Message, "738291456021") || !strings.Contains(string(e.Raw), "acme-prod-4821") {
		t.Errorf("the project is gone: %q %s", e.Message, e.Raw)
	}
}

func TestMaskGoogleHostErrors(t *testing.T) {
	vertexCall := sdk.Call{Account: protocol.Account{
		Settings: json.RawMessage(`{"base_url":"https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod-4821/locations/us-central1/endpoints/openapi"}`),
	}}
	t.Run("unreachable vertex url", func(t *testing.T) {
		e := &llm.Error{
			Code:    llm.CodeUnavailable,
			Scope:   llm.ScopeEndpoint,
			Message: `upstream unreachable: Post "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod-4821/locations/us-central1/endpoints/openapi/chat/completions": dial tcp 1.2.3.4:443: connect: connection refused`,
		}
		got := maskGoogle(failWith(e))(context.Background(), vertexCall, nil)
		var le *llm.Error
		if !errors.As(got, &le) {
			t.Fatalf("got %v, want *llm.Error", got)
		}
		if strings.Contains(le.Message, "acme-prod-4821") {
			t.Errorf("project leaked in message: %q", le.Message)
		}
		if !strings.Contains(le.Message, "/projects/[redacted]/locations/us-central1/endpoints/openapi/chat/completions") {
			t.Errorf("unexpected message: %q", le.Message)
		}
		if le.Code != llm.CodeUnavailable || le.Scope != llm.ScopeEndpoint {
			t.Errorf("classification changed: %+v", le)
		}
	})
	t.Run("known project from base_url on non-google body", func(t *testing.T) {
		e := &llm.Error{
			Code:    llm.CodePermission,
			Message: "denied for acme-prod-4821",
			Raw:     json.RawMessage(`{"detail":"denied for acme-prod-4821"}`),
		}
		got := maskGoogle(failWith(e))(context.Background(), vertexCall, nil)
		var le *llm.Error
		if !errors.As(got, &le) {
			t.Fatalf("got %v, want *llm.Error", got)
		}
		if strings.Contains(le.Message, "acme-prod-4821") || strings.Contains(string(le.Raw), "acme-prod-4821") {
			t.Errorf("project leaked: %q %s", le.Message, le.Raw)
		}
	})
	t.Run("bare googleapis.com host", func(t *testing.T) {
		call := sdk.Call{Account: protocol.Account{
			Settings: json.RawMessage(`{"base_url":"https://googleapis.com/v1/projects/acme-prod-4821/endpoints/openapi"}`),
		}}
		e := &llm.Error{Code: llm.CodeUnavailable, Message: `Post "https://googleapis.com/v1/projects/acme-prod-4821/endpoints/openapi/chat/completions": EOF`}
		got := maskGoogle(failWith(e))(context.Background(), call, nil)
		var le *llm.Error
		if !errors.As(got, &le) || strings.Contains(le.Message, "acme-prod-4821") {
			t.Errorf("got %+v, want masked error", got)
		}
	})
	t.Run("non-llm error on googleapis host", func(t *testing.T) {
		err := errors.New(`Post "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod-4821/locations/us-central1/endpoints/openapi": EOF`)
		got := maskGoogle(failWith(err))(context.Background(), vertexCall, nil)
		var le *llm.Error
		if !errors.As(got, &le) || strings.Contains(le.Message, "acme-prod-4821") || le.Code != llm.CodeInternal {
			t.Errorf("got %#v, want masked internal error", got)
		}
	})
	t.Run("schemeless googleapis base_url", func(t *testing.T) {
		call := sdk.Call{Account: protocol.Account{
			Settings: json.RawMessage(`{"base_url":"us-central1-aiplatform.googleapis.com/v1/projects/acme-prod-4821/locations/us-central1/endpoints/openapi"}`),
		}}
		e := &llm.Error{
			Code:    llm.CodeUnavailable,
			Message: `upstream unreachable: Post "us-central1-aiplatform.googleapis.com/v1/projects/acme-prod-4821/locations/us-central1/endpoints/openapi/models/x:generateContent": unsupported protocol scheme ""`,
		}
		got := maskGoogle(failWith(e))(context.Background(), call, nil)
		var le *llm.Error
		if !errors.As(got, &le) {
			t.Fatalf("got %v, want *llm.Error", got)
		}
		if strings.Contains(le.Message, "acme-prod-4821") {
			t.Errorf("project leaked in message: %q", le.Message)
		}
		if !strings.Contains(le.Message, "/projects/[redacted]/locations") {
			t.Errorf("unexpected message: %q", le.Message)
		}
	})
	t.Run("escaped project in base_url", func(t *testing.T) {
		call := sdk.Call{Account: protocol.Account{
			Settings: json.RawMessage(`{"base_url":"https://us-central1-aiplatform.googleapis.com/v1/projects/acme%2Dprod%2D4821/locations/us-central1/endpoints/openapi"}`),
		}}
		e := &llm.Error{
			Code:    llm.CodeUnavailable,
			Message: `upstream unreachable: Post "https://us-central1-aiplatform.googleapis.com/v1/projects/acme%2Dprod%2D4821/locations/us-central1/endpoints/openapi/chat/completions": connection refused`,
		}
		got := maskGoogle(failWith(e))(context.Background(), call, nil)
		var le *llm.Error
		if !errors.As(got, &le) {
			t.Fatalf("got %v, want *llm.Error", got)
		}
		if strings.Contains(le.Message, "acme%2Dprod%2D4821") || strings.Contains(le.Message, "acme-prod-4821") {
			t.Errorf("project leaked in message: %q", le.Message)
		}
		if !strings.Contains(le.Message, "/projects/[redacted]/locations") {
			t.Errorf("unexpected message: %q", le.Message)
		}
	})
	t.Run("schemeless other vendor host stays byte-identical", func(t *testing.T) {
		call := sdk.Call{Account: protocol.Account{
			Settings: json.RawMessage(`{"base_url":"api.example.com/v1/projects/acme-prod-4821/v1"}`),
		}}
		raw := `{"error":{"message":"Project acme-prod-4821 is over budget","type":"insufficient_quota","code":"insufficient_quota"}}`
		e := &llm.Error{Code: llm.CodeQuota, Message: "Project acme-prod-4821 is over budget", Raw: json.RawMessage(raw)}
		if got := maskGoogle(failWith(e))(context.Background(), call, nil); got != e || string(e.Raw) != raw {
			t.Errorf("got %+v, want byte-identical error", got)
		}
	})
	t.Run("other vendor host stays byte-identical", func(t *testing.T) {
		for _, base := range []string{
			"https://notgoogleapis.com/v1/projects/acme-prod-4821/v1",
			"https://api.example.com/v1/projects/acme-prod-4821/v1",
		} {
			call := sdk.Call{Account: protocol.Account{Settings: json.RawMessage(`{"base_url":"` + base + `"}`)}}
			raw := `{"error":{"message":"Project acme-prod-4821 at projects/acme-prod-4821 is over budget","type":"insufficient_quota","code":"insufficient_quota"}}`
			e := &llm.Error{Code: llm.CodeQuota, Message: "Project acme-prod-4821 at projects/acme-prod-4821 is over budget", Raw: json.RawMessage(raw)}
			if got := maskGoogle(failWith(e))(context.Background(), call, nil); got != e || string(e.Raw) != raw {
				t.Errorf("%s: got %+v, want byte-identical error", base, got)
			}
		}
	})
}
