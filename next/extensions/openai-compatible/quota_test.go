package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestQuotaPreset(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		quota   string
		want    string
		wantErr bool
	}{
		{name: "openrouter preset", baseURL: "https://openrouter.ai/api/v1", want: quotaOpenRouter},
		{name: "openrouter uppercase host", baseURL: "https://OPENROUTER.AI/api/v1", want: quotaOpenRouter},
		{name: "deepseek preset", baseURL: "https://api.deepseek.com/v1", want: quotaDeepSeek},
		{name: "deepseek root url", baseURL: "https://api.deepseek.com", want: quotaDeepSeek},
		{name: "other vendor preset", baseURL: "https://api.openai.com/v1", want: quotaNone},
		{name: "explicit openrouter on custom host", baseURL: "http://127.0.0.1:8080/api/v1", quota: "openrouter", want: quotaOpenRouter},
		{name: "explicit deepseek on custom host", baseURL: "http://127.0.0.1:8080/v1", quota: "deepseek", want: quotaDeepSeek},
		{name: "explicit none overrides openrouter host", baseURL: "https://openrouter.ai/api/v1", quota: "none", want: quotaNone},
		{name: "invalid quota setting", baseURL: "https://api.openai.com/v1", quota: "bogus", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := quotaVendor(settings{BaseURL: tc.baseURL, Quota: tc.quota})
			if tc.wantErr {
				var e *llm.Error
				if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest {
					t.Fatalf("quotaVendor() err = %v, want invalid_request", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("quotaVendor() = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestQuotaURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		vendor  string
		want    string
	}{
		{name: "deepseek root", baseURL: "https://api.deepseek.com", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek v1", baseURL: "https://api.deepseek.com/v1", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek beta", baseURL: "https://api.deepseek.com/beta", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek beta trailing slash", baseURL: "https://api.deepseek.com/beta/", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek beta v1", baseURL: "https://api.deepseek.com/beta/v1", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek anthropic", baseURL: "https://api.deepseek.com/anthropic", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "deepseek anthropic v1", baseURL: "https://api.deepseek.com/anthropic/v1", vendor: quotaDeepSeek, want: "https://api.deepseek.com/user/balance"},
		{name: "openrouter api v1", baseURL: "https://openrouter.ai/api/v1", vendor: quotaOpenRouter, want: "https://openrouter.ai/api/v1/key"},
		{name: "openrouter leaves beta alone", baseURL: "https://openrouter.ai/beta", vendor: quotaOpenRouter, want: "https://openrouter.ai/beta/api/v1/key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaURL(tc.baseURL, tc.vendor); got != tc.want {
				t.Fatalf("quotaURL(%q, %q) = %q, want %q", tc.baseURL, tc.vendor, got, tc.want)
			}
		})
	}
}

func TestParseOpenRouterQuota(t *testing.T) {
	t.Run("capped key with free model daily requests", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"label": "sk-or-v1-abc...xyz",
				"limit": 100,
				"limit_remaining": 74.5,
				"limit_reset": "monthly",
				"include_byok_in_limit": false,
				"usage": 25.5,
				"usage_daily": 1.2,
				"usage_weekly": 5.4,
				"usage_monthly": 25.5,
				"is_free_tier": false,
				"free_model_daily_requests": {
					"used": 12,
					"limit": 50,
					"remaining": 38
				}
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		if q.Plan != "paid" {
			t.Errorf("Plan = %q, want %q", q.Plan, "paid")
		}
		byID := map[string]protocol.QuotaLimit{}
		for _, l := range q.Limits {
			byID[l.ID] = l
		}
		if l, ok := byID["limit"]; !ok || l.Limit == nil || *l.Limit != 100 || l.Remaining == nil || *l.Remaining != 74.5 ||
			l.Used == nil || *l.Used != 25.5 || l.UsedPercent == nil || *l.UsedPercent != 25.5 || l.Unit != "usd" ||
			l.WindowSeconds != 30*86400 || l.Unlimited {
			t.Errorf("limit = %+v", l)
		}
		if l, ok := byID["usage_daily"]; !ok || l.Used == nil || *l.Used != 1.2 || l.Unit != "usd" || l.WindowSeconds != 86400 {
			t.Errorf("usage_daily = %+v", l)
		}
		if l, ok := byID["usage_weekly"]; !ok || l.Used == nil || *l.Used != 5.4 || l.Unit != "usd" || l.WindowSeconds != 7*86400 {
			t.Errorf("usage_weekly = %+v", l)
		}
		if l, ok := byID["usage_monthly"]; !ok || l.Used == nil || *l.Used != 25.5 || l.Unit != "usd" || l.WindowSeconds != 30*86400 {
			t.Errorf("usage_monthly = %+v", l)
		}
		if l, ok := byID["free_model_daily_requests"]; !ok || l.Used == nil || *l.Used != 12 || l.Limit == nil || *l.Limit != 50 ||
			l.Remaining == nil || *l.Remaining != 38 || l.UsedPercent == nil || *l.UsedPercent != 24 || l.Unit != "requests" ||
			l.WindowSeconds != 86400 {
			t.Errorf("free_model_daily_requests = %+v", l)
		}
		if !json.Valid(q.Raw) {
			t.Errorf("Raw is not valid JSON: %s", q.Raw)
		}
	})

	t.Run("unlimited free tier key", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"label": "sk-or-v1-free",
				"limit": null,
				"limit_remaining": null,
				"limit_reset": null,
				"usage": 0,
				"usage_daily": 0,
				"usage_weekly": 0,
				"usage_monthly": 0,
				"is_free_tier": true
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		if q.Plan != "free" {
			t.Errorf("Plan = %q, want %q", q.Plan, "free")
		}
		byID := map[string]protocol.QuotaLimit{}
		for _, l := range q.Limits {
			byID[l.ID] = l
		}
		if l, ok := byID["limit"]; !ok || !l.Unlimited || l.Used == nil || *l.Used != 0 || l.Unit != "usd" {
			t.Errorf("limit = %+v", l)
		}
		if _, ok := byID["free_model_daily_requests"]; ok {
			t.Errorf("unexpected free_model_daily_requests when absent from reply")
		}
	})

	t.Run("weekly key example with effective_budget", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"label": "sk-or-v1-...6ed",
				"is_management_key": false,
				"is_provisioning_key": false,
				"limit": 500,
				"limit_reset": "weekly",
				"limit_remaining": 406.54343450199997,
				"include_byok_in_limit": false,
				"budgets": [],
				"effective_budget": {
					"scope": "key_limit",
					"guardrail_id": null,
					"budget_id": null,
					"limit_usd": 500,
					"spend_usd": 93.456565498,
					"remaining_usd": 406.54343450199997,
					"reset_interval": "weekly",
					"resets_at": "2026-11-23T00:00:00.000Z",
					"include_byok": false
				},
				"usage": 420.006262049,
				"usage_daily": 93.456565498,
				"usage_weekly": 93.456565498,
				"usage_monthly": 420.006262049,
				"is_free_tier": false,
				"free_model_daily_requests": {
					"used": 1004,
					"limit": 1000,
					"remaining": 0
				}
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		byID := map[string]protocol.QuotaLimit{}
		for _, l := range q.Limits {
			byID[l.ID] = l
		}
		l, ok := byID["limit"]
		if !ok {
			t.Fatalf("missing limit entry in %+v", q.Limits)
		}
		wantReset := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC).Unix()
		wantPct := (500 - 406.54343450199997) / 500 * 100
		if l.Unlimited || l.Unit != "usd" || l.WindowSeconds != 7*86400 || l.ResetsAt != wantReset ||
			l.Limit == nil || *l.Limit != 500 ||
			l.Remaining == nil || *l.Remaining != 406.54343450199997 ||
			l.Used == nil || math.Abs(*l.Used-93.456565498) > 1e-9 ||
			l.UsedPercent == nil || math.Abs(*l.UsedPercent-wantPct) > 1e-6 {
			t.Errorf("limit = %+v (used=%v, pct=%v)", l, l.Used, l.UsedPercent)
		}
	})

	t.Run("weekly key without effective_budget uses window spend not all-time usage", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"limit": 500,
				"limit_reset": "weekly",
				"limit_remaining": 406.54343450199997,
				"usage": 420.006262049,
				"usage_daily": 93.456565498,
				"usage_weekly": 93.456565498,
				"usage_monthly": 420.006262049,
				"is_free_tier": false
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		byID := map[string]protocol.QuotaLimit{}
		for _, l := range q.Limits {
			byID[l.ID] = l
		}
		l, ok := byID["limit"]
		if !ok {
			t.Fatalf("missing limit entry in %+v", q.Limits)
		}
		wantUsed := 500 - 406.54343450199997
		wantPct := wantUsed / 500 * 100
		if l.Unlimited || l.Unit != "usd" || l.WindowSeconds != 7*86400 || l.ResetsAt != 0 ||
			l.Limit == nil || *l.Limit != 500 ||
			l.Remaining == nil || *l.Remaining != 406.54343450199997 ||
			l.Used == nil || math.Abs(*l.Used-wantUsed) > 1e-9 ||
			l.UsedPercent == nil || math.Abs(*l.UsedPercent-wantPct) > 1e-6 {
			t.Errorf("limit = %+v", l)
		}
	})

	t.Run("windowed key without limit_remaining leaves used unset", func(t *testing.T) {
		t.Run("with numeric limit", func(t *testing.T) {
			raw := []byte(`{
				"data": {
					"limit": 500,
					"limit_reset": "weekly",
					"limit_remaining": null,
					"usage": 420.006262049
				}
			}`)
			q, err := parseOpenRouterQuota(raw)
			if err != nil {
				t.Fatalf("parseOpenRouterQuota: %v", err)
			}
			if len(q.Limits) != 1 {
				t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
			}
			l := q.Limits[0]
			if l.ID != "limit" || l.Unlimited || l.WindowSeconds != 7*86400 ||
				l.Limit == nil || *l.Limit != 500 ||
				l.Used != nil || l.Remaining != nil || l.UsedPercent != nil {
				t.Errorf("limit = %+v", l)
			}
		})
		t.Run("without numeric limit", func(t *testing.T) {
			raw := []byte(`{
				"data": {
					"limit": null,
					"limit_reset": "weekly",
					"limit_remaining": null,
					"usage": 420.006262049
				}
			}`)
			q, err := parseOpenRouterQuota(raw)
			if err != nil {
				t.Fatalf("parseOpenRouterQuota: %v", err)
			}
			if len(q.Limits) != 1 {
				t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
			}
			l := q.Limits[0]
			if l.ID != "limit" || !l.Unlimited || l.WindowSeconds != 7*86400 ||
				l.Limit != nil || l.Used != nil || l.Remaining != nil || l.UsedPercent != nil {
				t.Errorf("limit = %+v", l)
			}
		})
	})

	t.Run("non-windowed key without limit_remaining keeps usage fallback", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"limit": 100,
				"limit_reset": null,
				"limit_remaining": null,
				"usage": 25.5
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		l := q.Limits[0]
		if l.ID != "limit" || l.Unlimited || l.WindowSeconds != 0 ||
			l.Limit == nil || *l.Limit != 100 ||
			l.Used == nil || *l.Used != 25.5 ||
			l.Remaining == nil || *l.Remaining != 74.5 ||
			l.UsedPercent == nil || *l.UsedPercent != 25.5 {
			t.Errorf("limit = %+v", l)
		}
	})

	t.Run("null key limit with effective_budget is not unlimited", func(t *testing.T) {
		raw := []byte(`{
			"data": {
				"limit": null,
				"limit_remaining": null,
				"budgets": [
					{
						"scope": "workspace_default_member",
						"guardrail_id": "0df9e665-d932-5740-b2c7-b52af166bc11",
						"budget_id": null,
						"limit_usd": 100,
						"spend_usd": 40,
						"remaining_usd": 60,
						"reset_interval": "weekly",
						"resets_at": "2026-10-12T00:00:00.000Z",
						"include_byok": false
					},
					{
						"scope": "member",
						"guardrail_id": "6b1d2c3e-4f50-4a61-9b72-83c4d5e6f708",
						"budget_id": null,
						"limit_usd": 50,
						"spend_usd": 40,
						"remaining_usd": 10,
						"reset_interval": "weekly",
						"resets_at": "2026-10-12T00:00:00.000Z",
						"include_byok": false
					}
				],
				"effective_budget": {
					"scope": "member",
					"guardrail_id": "6b1d2c3e-4f50-4a61-9b72-83c4d5e6f708",
					"budget_id": null,
					"limit_usd": 50,
					"spend_usd": 40,
					"remaining_usd": 10,
					"reset_interval": "weekly",
					"resets_at": "2026-10-12T00:00:00.000Z",
					"include_byok": false
				}
			}
		}`)
		q, err := parseOpenRouterQuota(raw)
		if err != nil {
			t.Fatalf("parseOpenRouterQuota: %v", err)
		}
		if len(q.Limits) != 1 {
			t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
		}
		l := q.Limits[0]
		wantReset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC).Unix()
		if l.ID != "limit" || l.Unlimited || l.Unit != "usd" ||
			l.Limit == nil || *l.Limit != 50 ||
			l.Used == nil || *l.Used != 40 ||
			l.Remaining == nil || *l.Remaining != 10 ||
			l.UsedPercent == nil || *l.UsedPercent != 80 ||
			l.WindowSeconds != 7*86400 || l.ResetsAt != wantReset {
			t.Errorf("limit = %+v", l)
		}
	})

	t.Run("spent and lifetime effective_budget", func(t *testing.T) {
		t.Run("spent over limit", func(t *testing.T) {
			raw := []byte(`{
				"data": {
					"limit": null,
					"limit_remaining": null,
					"effective_budget": {
						"scope": "member",
						"limit_usd": 50,
						"spend_usd": 52,
						"remaining_usd": -2,
						"reset_interval": "weekly",
						"resets_at": "2026-10-12T00:00:00.000Z"
					}
				}
			}`)
			q, err := parseOpenRouterQuota(raw)
			if err != nil {
				t.Fatalf("parseOpenRouterQuota: %v", err)
			}
			if len(q.Limits) != 1 {
				t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
			}
			l := q.Limits[0]
			wantReset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC).Unix()
			if l.ID != "limit" || l.Unlimited ||
				l.Limit == nil || *l.Limit != 50 ||
				l.Used == nil || *l.Used != 52 ||
				l.Remaining == nil || *l.Remaining != -2 ||
				l.UsedPercent == nil || *l.UsedPercent != 100 ||
				l.WindowSeconds != 7*86400 || l.ResetsAt != wantReset {
				t.Errorf("limit = %+v", l)
			}
		})
		t.Run("lifetime workspace budget", func(t *testing.T) {
			raw := []byte(`{
				"data": {
					"limit": null,
					"limit_remaining": null,
					"effective_budget": {
						"scope": "workspace",
						"budget_id": "b-1",
						"limit_usd": 200,
						"spend_usd": 50,
						"remaining_usd": 150,
						"reset_interval": null,
						"resets_at": null
					}
				}
			}`)
			q, err := parseOpenRouterQuota(raw)
			if err != nil {
				t.Fatalf("parseOpenRouterQuota: %v", err)
			}
			if len(q.Limits) != 1 {
				t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
			}
			l := q.Limits[0]
			if l.ID != "limit" || l.Unlimited ||
				l.Limit == nil || *l.Limit != 200 ||
				l.Used == nil || *l.Used != 50 ||
				l.Remaining == nil || *l.Remaining != 150 ||
				l.UsedPercent == nil || *l.UsedPercent != 25 ||
				l.WindowSeconds != 0 || l.ResetsAt != 0 {
				t.Errorf("limit = %+v", l)
			}
		})
		t.Run("malformed resets_at leaves ResetsAt unset", func(t *testing.T) {
			raw := []byte(`{
				"data": {
					"effective_budget": {
						"scope": "member",
						"limit_usd": 50,
						"spend_usd": 10,
						"remaining_usd": 40,
						"reset_interval": "daily",
						"resets_at": "not-a-timestamp"
					}
				}
			}`)
			q, err := parseOpenRouterQuota(raw)
			if err != nil {
				t.Fatalf("parseOpenRouterQuota: %v", err)
			}
			if len(q.Limits) != 1 {
				t.Fatalf("Limits = %+v, want 1 entry", q.Limits)
			}
			l := q.Limits[0]
			if l.ID != "limit" || l.ResetsAt != 0 || l.WindowSeconds != 86400 || l.Remaining == nil || *l.Remaining != 40 {
				t.Errorf("limit = %+v", l)
			}
		})
	})

	t.Run("invalid reply", func(t *testing.T) {
		for _, raw := range [][]byte{[]byte(`not json`), []byte(`{}`), []byte(`{"data":null}`), []byte(`{"data":{"effective_budget":"bad"}}`)} {
			_, err := parseOpenRouterQuota(raw)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
				t.Errorf("parseOpenRouterQuota(%s) = %v, want upstream error", raw, err)
			}
		}
	})
}

func TestParseDeepSeekQuota(t *testing.T) {
	t.Run("available with cny and usd", func(t *testing.T) {
		raw := []byte(`{
			"is_available": true,
			"balance_infos": [
				{
					"currency": "CNY",
					"total_balance": "110.00",
					"granted_balance": "10.00",
					"topped_up_balance": "100.00"
				},
				{
					"currency": "USD",
					"total_balance": "12.50",
					"granted_balance": "0.00",
					"topped_up_balance": "12.50"
				}
			]
		}`)
		q, err := parseDeepSeekQuota(raw)
		if err != nil {
			t.Fatalf("parseDeepSeekQuota: %v", err)
		}
		if q.Plan != "" {
			t.Errorf("Plan = %q, want empty", q.Plan)
		}
		if len(q.Limits) != 2 {
			t.Fatalf("Limits = %+v, want 2 entries", q.Limits)
		}
		if l := q.Limits[0]; l.ID != "balance_cny" || l.Label != "Balance (CNY)" || l.Remaining == nil || *l.Remaining != 110 || l.Unit != "cny" {
			t.Errorf("Limits[0] = %+v", l)
		}
		if l := q.Limits[1]; l.ID != "balance_usd" || l.Label != "Balance (USD)" || l.Remaining == nil || *l.Remaining != 12.5 || l.Unit != "usd" {
			t.Errorf("Limits[1] = %+v", l)
		}
		if !json.Valid(q.Raw) {
			t.Errorf("Raw is not valid JSON: %s", q.Raw)
		}
	})

	t.Run("insufficient balance", func(t *testing.T) {
		raw := []byte(`{
			"is_available": false,
			"balance_infos": [
				{
					"currency": "CNY",
					"total_balance": "0.00",
					"granted_balance": "0.00",
					"topped_up_balance": "0.00"
				}
			]
		}`)
		q, err := parseDeepSeekQuota(raw)
		if err != nil {
			t.Fatalf("parseDeepSeekQuota: %v", err)
		}
		if q.Plan != "" {
			t.Errorf("Plan = %q, want empty", q.Plan)
		}
		if len(q.Limits) != 1 || q.Limits[0].Remaining == nil || *q.Limits[0].Remaining != 0 {
			t.Errorf("Limits = %+v", q.Limits)
		}
	})

	t.Run("invalid reply", func(t *testing.T) {
		for _, raw := range [][]byte{[]byte(`not json`), []byte(`{}`), []byte(`{"is_available":true,"balance_infos":[]}`)} {
			_, err := parseDeepSeekQuota(raw)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
				t.Errorf("parseDeepSeekQuota(%s) = %v, want upstream error", raw, err)
			}
		}
	})
}

func TestQuotaCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer made-up-quota-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"bad key","type":"invalid_request_error"}}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/key":
			io.WriteString(w, `{"data":{"limit":50,"limit_remaining":20,"usage":30,"usage_daily":2,"usage_weekly":10,"usage_monthly":30,"is_free_tier":false}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/user/balance":
			io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"42.00","granted_balance":"0.00","topped_up_balance":"42.00"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	callFor := func(set map[string]string, key string) sdk.QuotaCall {
		raw, _ := json.Marshal(set)
		return sdk.QuotaCall{
			ID: "q-1",
			Account: protocol.Account{
				ID:         "acct",
				Settings:   raw,
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key},
			},
		}
	}

	t.Run("openrouter", func(t *testing.T) {
		q, err := quota(context.Background(), callFor(map[string]string{"base_url": srv.URL + "/api/v1", "quota": "openrouter"}, "made-up-quota-key"))
		if err != nil {
			t.Fatalf("quota: %v", err)
		}
		if q.Plan != "paid" || len(q.Limits) != 4 || q.Limits[0].ID != "limit" || *q.Limits[0].Remaining != 20 {
			t.Fatalf("quota = %+v", q)
		}
	})

	t.Run("deepseek", func(t *testing.T) {
		q, err := quota(context.Background(), callFor(map[string]string{"base_url": srv.URL + "/v1", "quota": "deepseek"}, "made-up-quota-key"))
		if err != nil {
			t.Fatalf("quota: %v", err)
		}
		if q.Plan != "" || len(q.Limits) != 1 || q.Limits[0].ID != "balance_usd" || *q.Limits[0].Remaining != 42 {
			t.Fatalf("quota = %+v", q)
		}
	})

	t.Run("deepseek beta", func(t *testing.T) {
		q, err := quota(context.Background(), callFor(map[string]string{"base_url": srv.URL + "/beta", "quota": "deepseek"}, "made-up-quota-key"))
		if err != nil {
			t.Fatalf("quota: %v", err)
		}
		if q.Plan != "" || len(q.Limits) != 1 || q.Limits[0].ID != "balance_usd" || *q.Limits[0].Remaining != 42 {
			t.Fatalf("quota = %+v", q)
		}
	})

	t.Run("none", func(t *testing.T) {
		_, err := quota(context.Background(), callFor(map[string]string{"base_url": srv.URL + "/v1"}, "made-up-quota-key"))
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
			t.Fatalf("none quota err = %v, want unsupported credential", err)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		_, err := quota(context.Background(), callFor(map[string]string{"base_url": srv.URL + "/api/v1", "quota": "openrouter"}, "wrong-key"))
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeAuth {
			t.Fatalf("unauthorized quota err = %v, want auth", err)
		}
	})
}
