package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestQuotaBeforeRateLimit(t *testing.T) {
	srv := geminitest.New(t)
	for _, tc := range []struct {
		name string
		acct protocol.Account
	}{
		{
			name: "gemini api key",
			acct: protocol.Account{
				ID:         "quota-empty-gemini",
				Settings:   settingsFor(srv, kindGemini),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
			},
		},
		{
			name: "vertex service account",
			acct: protocol.Account{
				ID:         "quota-empty-vertex-sa",
				Settings:   settingsFor(srv, kindVertexSA),
				Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("p", true)},
			},
		},
		{
			name: "vertex api key",
			acct: protocol.Account{
				ID:         "quota-empty-vertex-key",
				Settings:   settingsFor(srv, kindVertexKey),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: tc.acct})
			if err != nil {
				t.Fatalf("quota before 429 failed: %v", err)
			}
			if q == nil || q.Limits == nil || len(q.Limits) != 0 {
				t.Fatalf("quota = %+v, want non-nil empty limits slice before any 429", q)
			}
			lowPlan := strings.ToLower(q.Plan)
			if !strings.Contains(lowPlan, "no rate-limit") || !strings.Contains(lowPlan, "last-seen") {
				t.Errorf("plan = %q, want it to state no rate-limit reply seen yet and last-seen caveat", q.Plan)
			}
			var raw struct {
				Source   string `json:"source"`
				Observed bool   `json:"observed"`
				Note     string `json:"note"`
			}
			if err := json.Unmarshal(q.Raw, &raw); err != nil {
				t.Fatalf("raw = %s, unmarshal error: %v", q.Raw, err)
			}
			if raw.Source != "last_429" || raw.Observed || raw.Note == "" {
				t.Errorf("raw = %+v, want source=last_429, observed=false, and note", raw)
			}
		})
	}

	t.Run("bad settings", func(t *testing.T) {
		_, err := quota(context.Background(), sdk.QuotaCall{
			ID:      "q",
			Account: protocol.Account{ID: "bad-set", Settings: json.RawMessage(`{`)},
		})
		var le *llm.Error
		if !errors.As(err, &le) || le.Code != llm.CodeInvalidRequest {
			t.Fatalf("err = %v, want invalid_request", err)
		}
	})
}

func TestQuotaParsesQuotaFailureRetryInfoErrorInfo(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{
			"error": {
				"code": 429,
				"message": "You exceeded your current quota, please check your plan and billing details.",
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{
						"@type": "type.googleapis.com/google.rpc.ErrorInfo",
						"reason": "RATE_LIMIT_EXCEEDED",
						"domain": "generativelanguage.googleapis.com",
						"metadata": {
							"model": "gemini-2.5-flash",
							"service": "generativelanguage.googleapis.com"
						}
					},
					{
						"@type": "type.googleapis.com/google.rpc.QuotaFailure",
						"violations": [
							{
								"subject": "project_number:123",
								"description": "GenerateContent request limit per model per minute",
								"apiService": "generativelanguage.googleapis.com",
								"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests",
								"quotaId": "GenerateRequestsPerMinutePerProjectPerModel-FreeTier",
								"quotaDimensions": {"location": "global", "model": "gemini-2.5-flash"},
								"quotaValue": "15"
							},
							{
								"apiService": "generativelanguage.googleapis.com",
								"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_input_token_count",
								"quotaId": "GenerateContentInputTokensPerModelPerMinute-FreeTier",
								"quotaDimensions": {"location": "global", "model": "gemini-2.5-flash"},
								"quotaValue": 250000
							}
						]
					},
					{
						"@type": "type.googleapis.com/google.rpc.RetryInfo",
						"retryDelay": "44.2s"
					}
				]
			}
		}`)
	}

	acct := protocol.Account{
		ID:         "quota-parsed",
		Settings:   settingsFor(srv, kindGemini),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
	call := sdk.Call{
		Account: acct,
		Request: llm.Request{
			Model:  "gemini-2.5-flash",
			Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)},
		},
	}

	before := time.Now().UTC()
	if err := generateTo(context.Background(), call, &recorder{}); err == nil {
		t.Fatal("generateTo succeeded, want 429 error")
	}
	after := time.Now().UTC()

	q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
	if err != nil {
		t.Fatalf("quota failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(q.Plan), "last-seen") || !strings.Contains(strings.ToLower(q.Plan), "not a live reading") {
		t.Errorf("plan = %q, want last-seen / not a live reading caveat", q.Plan)
	}
	if !strings.Contains(q.Plan, "RATE_LIMIT_EXCEEDED") {
		t.Errorf("plan = %q, want ErrorInfo reason RATE_LIMIT_EXCEEDED", q.Plan)
	}
	if len(q.Limits) != 2 {
		t.Fatalf("len(limits) = %d, want 2: %+v", len(q.Limits), q.Limits)
	}

	l0 := q.Limits[0]
	if l0.ID != "GenerateRequestsPerMinutePerProjectPerModel-FreeTier" {
		t.Errorf("limits[0].ID = %q", l0.ID)
	}
	if !strings.Contains(strings.ToLower(l0.Label), "last seen") || !strings.Contains(strings.ToLower(l0.Label), "not a live reading") {
		t.Errorf("limits[0].Label = %q, want last-seen caveat", l0.Label)
	}
	if l0.Limit == nil || *l0.Limit != 15 || l0.Unit != "requests" {
		t.Errorf("limits[0] limit/unit = %v %q, want 15 requests", l0.Limit, l0.Unit)
	}
	if l0.WindowSeconds != 60 {
		t.Errorf("limits[0].WindowSeconds = %d, want 60", l0.WindowSeconds)
	}
	if l0.ResetsAt < before.Unix()+45 || l0.ResetsAt > after.Unix()+45 {
		t.Errorf("limits[0].ResetsAt = %d, want between %d and %d", l0.ResetsAt, before.Unix()+45, after.Unix()+45)
	}
	if len(l0.Models) != 1 || l0.Models[0] != "gemini-2.5-flash" {
		t.Errorf("limits[0].Models = %v, want [gemini-2.5-flash]", l0.Models)
	}
	if l0.UsedPercent != nil || l0.Used != nil || l0.Remaining != nil {
		t.Errorf("limits[0] unexpectedly fabricated usage counts: %+v", l0)
	}

	l1 := q.Limits[1]
	if l1.ID != "GenerateContentInputTokensPerModelPerMinute-FreeTier" {
		t.Errorf("limits[1].ID = %q", l1.ID)
	}
	if l1.Limit == nil || *l1.Limit != 250000 || l1.Unit != "tokens" {
		t.Errorf("limits[1] limit/unit = %v %q, want 250000 tokens", l1.Limit, l1.Unit)
	}

	var raw struct {
		Source       string            `json:"source"`
		Observed     bool              `json:"observed"`
		ObservedAt   time.Time         `json:"observed_at"`
		Status       string            `json:"status"`
		RetryDelayMS int64             `json:"retry_delay_ms"`
		ResetsAt     int64             `json:"resets_at"`
		ErrorInfo    map[string]any    `json:"error_info"`
		Violations   []json.RawMessage `json:"violations"`
	}
	if err := json.Unmarshal(q.Raw, &raw); err != nil {
		t.Fatalf("raw = %s, unmarshal: %v", q.Raw, err)
	}
	if raw.Source != "last_429" || !raw.Observed || raw.Status != "RESOURCE_EXHAUSTED" || raw.RetryDelayMS != 44200 || len(raw.Violations) != 2 || raw.ErrorInfo["reason"] != "RATE_LIMIT_EXCEEDED" {
		t.Errorf("unexpected raw payload: %+v", raw)
	}
}

func TestQuotaPerAccountIsolationAndOverwrite(t *testing.T) {
	srv := geminitest.New(t)
	callN := 0
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		callN++
		if callN == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"RequestsPerDay-limit","quotaValue":"100"}]}]}}`)
			return
		}
		geminitest.RateLimited(w, 20)
	}

	acctA := protocol.Account{
		ID:         "quota-iso-a",
		Settings:   settingsFor(srv, kindGemini),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
	acctB := protocol.Account{
		ID:         "quota-iso-b",
		Settings:   settingsFor(srv, kindGemini),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
	req := llm.Request{
		Model:  "gemini-2.5-pro",
		Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)},
	}

	_ = generateTo(context.Background(), sdk.Call{Account: acctA, Request: req}, &recorder{})

	qB, err := quota(context.Background(), sdk.QuotaCall{ID: "qb", Account: acctB})
	if err != nil {
		t.Fatal(err)
	}
	if len(qB.Limits) != 0 {
		t.Fatalf("account B saw account A's 429: %+v", qB.Limits)
	}

	qA1, err := quota(context.Background(), sdk.QuotaCall{ID: "qa1", Account: acctA})
	if err != nil {
		t.Fatal(err)
	}
	if len(qA1.Limits) != 1 || qA1.Limits[0].ID != "RequestsPerDay-limit" || qA1.Limits[0].WindowSeconds != 86400 {
		t.Fatalf("first 429 on A = %+v", qA1.Limits)
	}

	// Second 429 replaces the first rather than merging stale violations.
	_ = generateTo(context.Background(), sdk.Call{Account: acctA, Request: req}, &recorder{})
	qA2, err := quota(context.Background(), sdk.QuotaCall{ID: "qa2", Account: acctA})
	if err != nil {
		t.Fatal(err)
	}
	if len(qA2.Limits) != 1 || qA2.Limits[0].ID == "RequestsPerDay-limit" || qA2.Limits[0].ResetsAt == 0 {
		t.Fatalf("second 429 did not replace first on A: %+v", qA2.Limits)
	}
}

func TestQuotaRateLimitWithoutQuotaFailureAndHeaderFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headers   map[string]string
		body      string
		wantID    string
		wantLimit *float64
		wantReset bool
	}{
		{
			name: "ErrorInfo quota_limit and Retry-After header",
			headers: map[string]string{
				"Retry-After": "30",
			},
			body: `{
				"error": {
					"code": 429,
					"status": "RESOURCE_EXHAUSTED",
					"details": [{
						"@type": "type.googleapis.com/google.rpc.ErrorInfo",
						"reason": "QUOTA_EXCEEDED",
						"domain": "googleapis.com",
						"metadata": {"quota_limit": "RequestsPerMinutePerProject", "quota_limit_value": "60"}
					}]
				}
			}`,
			wantID:    "RequestsPerMinutePerProject",
			wantLimit: ptrFloat(60),
			wantReset: true,
		},
		{
			name:      "empty 429 body still records timestamped rate limit",
			body:      `{}`,
			wantID:    "rate_limit",
			wantReset: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := geminitest.New(t)
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, tc.body)
			}
			acct := protocol.Account{
				ID:         "quota-fallback-" + tc.name,
				Settings:   settingsFor(srv, kindGemini),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
			}
			call := sdk.Call{
				Account: acct,
				Request: llm.Request{
					Model:  "gemini-2.5-flash",
					Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)},
				},
			}
			_ = generateTo(context.Background(), call, &recorder{})
			q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
			if err != nil {
				t.Fatal(err)
			}
			if len(q.Limits) != 1 || q.Limits[0].ID != tc.wantID {
				t.Fatalf("limits = %+v, want id %q", q.Limits, tc.wantID)
			}
			if (q.Limits[0].ResetsAt > 0) != tc.wantReset {
				t.Errorf("resets_at = %d, wantReset=%v", q.Limits[0].ResetsAt, tc.wantReset)
			}
			if tc.wantLimit != nil && (q.Limits[0].Limit == nil || *q.Limits[0].Limit != *tc.wantLimit) {
				t.Errorf("limit = %v, want %v", q.Limits[0].Limit, *tc.wantLimit)
			}
		})
	}
}

func TestQuotaDuplicateViolationIDsAndVertexArrayAndRedaction(t *testing.T) {
	t.Run("duplicate quotaId across dimensions stays unique", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `[{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[`+
				`{"quotaId":"SharedQuota","quotaDimensions":{"location":"us-central1"},"quotaValue":"10"},`+
				`{"quotaId":"SharedQuota","quotaDimensions":{"location":"europe-west1"},"quotaValue":"20"}`+
				`]}]}}]`)
		}
		acct := protocol.Account{
			ID:         "quota-dup-id",
			Settings:   settingsFor(srv, kindVertexKey),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
		}
		_ = generateTo(context.Background(), sdk.Call{
			Account: acct,
			Request: llm.Request{Model: "gemini-2.5-flash", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}},
		}, &recorder{})
		q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
		if err != nil {
			t.Fatal(err)
		}
		if len(q.Limits) != 2 || q.Limits[0].ID == q.Limits[1].ID || q.Limits[0].ID == "" || q.Limits[1].ID == "" {
			t.Fatalf("limits did not keep unique non-empty ids: %+v", q.Limits)
		}
	})

	t.Run("service account token echoed in 429 is redacted", func(t *testing.T) {
		srv := geminitest.New(t)
		var echoedToken string
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			echoedToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":{"code":429,"message":"quota exceeded for %s","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"subject":"%s","quotaId":"RequestsPerMinute","quotaValue":"5"}]}]}}`, echoedToken, echoedToken)
		}
		acct := protocol.Account{
			ID:         "quota-sa-redact",
			Settings:   settingsFor(srv, kindVertexSA),
			Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("p", true)},
		}
		_ = generateTo(context.Background(), sdk.Call{
			Account: acct,
			Request: llm.Request{Model: "gemini-2.5-flash", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}},
		}, &recorder{})
		if echoedToken == "" {
			t.Fatal("expected service account token in request")
		}
		q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		if strings.Contains(string(b), echoedToken) {
			t.Fatalf("quota response leaked service account token %q: %s", echoedToken, b)
		}
	})

	t.Run("stream embedded 429 error records quota", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"error":{"code":429,"message":"Stream quota exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"StreamRequestsPerMinute","quotaValue":"12"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`+"\n\n")
		}
		acct := protocol.Account{
			ID:         "quota-stream-429",
			Settings:   settingsFor(srv, kindGemini),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
		}
		_ = generateTo(context.Background(), sdk.Call{
			Account: acct,
			Request: llm.Request{Model: "gemini-2.5-flash", Stream: true, Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}},
		}, &recorder{})
		q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
		if err != nil {
			t.Fatal(err)
		}
		if len(q.Limits) != 1 || q.Limits[0].ID != "StreamRequestsPerMinute" || q.Limits[0].Limit == nil || *q.Limits[0].Limit != 12 || q.Limits[0].ResetsAt == 0 {
			t.Fatalf("stream embedded 429 quota = %+v", q.Limits)
		}
	})
}

func TestQuotaConcurrentAccess(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		geminitest.RateLimited(w, 15)
	}
	acct := protocol.Account{
		ID:         "quota-concurrent",
		Settings:   settingsFor(srv, kindGemini),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
	call := sdk.Call{
		Account: acct,
		Request: llm.Request{Model: "gemini-2.5-flash", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}},
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				_ = generateTo(context.Background(), call, &recorder{})
				if _, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct}); err != nil {
					t.Errorf("concurrent quota error: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}

func ptrFloat(v float64) *float64 { return &v }
