package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestOpenAICompatibleQuota checks GET /v0/management/accounts/{id}/quota for OpenAI-compatible
// accounts configured for OpenRouter (GET /api/v1/key), DeepSeek (GET /user/balance), and a vendor
// with no quota endpoint.
func TestOpenAICompatibleQuota(t *testing.T) {
	const (
		orKey   = "made-up-or-quota-key"
		dsKey   = "made-up-ds-quota-key"
		noneKey = "made-up-no-quota-key"
	)
	var orCalls, dsCalls, noneCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer "+noneKey && r.URL.Path != "/v1/models" {
			noneCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/v1/models" || r.URL.Path == "/api/v1/models"):
			io.WriteString(w, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/key":
			orCalls.Add(1)
			if auth != "Bearer "+orKey {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"code":401,"message":"Invalid API key"}}`)
				return
			}
			io.WriteString(w, `{
				"data": {
					"label": "sk-or-v1-e2e...123",
					"limit": 100,
					"limit_remaining": 74.5,
					"limit_reset": "monthly",
					"include_byok_in_limit": false,
					"usage": 25.5,
					"usage_daily": 1.2,
					"usage_weekly": 5.4,
					"usage_monthly": 25.5,
					"byok_usage": 0,
					"byok_usage_daily": 0,
					"byok_usage_weekly": 0,
					"byok_usage_monthly": 0,
					"is_free_tier": false,
					"free_model_daily_requests": {
						"used": 12,
						"limit": 50,
						"remaining": 38
					}
				}
			}`)
		case r.Method == http.MethodGet && r.URL.Path == "/user/balance":
			dsCalls.Add(1)
			if auth != "Bearer "+dsKey {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"message":"Authentication Fails","type":"authentication_error"}}`)
				return
			}
			io.WriteString(w, `{
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
						"total_balance": "25.00",
						"granted_balance": "0.00",
						"topped_up_balance": "25.00"
					}
				]
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "openrouter",
					"settings":   map[string]any{"base_url": srv.URL + "/api/v1", "quota": "openrouter"},
					"credential": map[string]any{"kind": "api_key", "api_key": orKey},
				},
				map[string]any{
					"id":         "deepseek",
					"settings":   map[string]any{"base_url": srv.URL + "/v1", "quota": "deepseek"},
					"credential": map[string]any{"kind": "api_key", "api_key": dsKey},
				},
				map[string]any{
					"id":         "none",
					"settings":   map[string]any{"base_url": srv.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": noneKey},
				},
			},
		},
	})

	type quotaLimit struct {
		ID            string   `json:"id"`
		Label         string   `json:"label"`
		UsedPercent   *float64 `json:"used_percent"`
		Used          *float64 `json:"used"`
		Limit         *float64 `json:"limit"`
		Remaining     *float64 `json:"remaining"`
		Unit          string   `json:"unit"`
		Unlimited     bool     `json:"unlimited"`
		WindowSeconds int64    `json:"window_seconds"`
	}
	type quotaReply struct {
		Quota struct {
			Plan   string         `json:"plan"`
			Limits []quotaLimit   `json:"limits"`
			Raw    map[string]any `json:"raw"`
		} `json:"quota"`
		FetchedAt time.Time                       `json:"fetched_at"`
		Error     *struct{ Code, Message string } `json:"error"`
	}

	t.Run("openrouter", func(t *testing.T) {
		var q quotaReply
		if code := manage(t, base, "GET", "/accounts/openrouter/quota", nil, &q); code != http.StatusOK {
			t.Fatalf("status = %d (%+v), want 200", code, q.Error)
		}
		if orCalls.Load() != 1 {
			t.Fatalf("openrouter /api/v1/key calls = %d, want 1", orCalls.Load())
		}
		if q.Quota.Plan != "paid" {
			t.Errorf("plan = %q, want %q", q.Quota.Plan, "paid")
		}
		byID := map[string]quotaLimit{}
		for _, l := range q.Quota.Limits {
			byID[l.ID] = l
		}
		if l, ok := byID["limit"]; !ok || l.Limit == nil || *l.Limit != 100 || l.Remaining == nil || *l.Remaining != 74.5 ||
			l.Used == nil || *l.Used != 25.5 || l.UsedPercent == nil || *l.UsedPercent != 25.5 || l.Unit != "usd" || l.WindowSeconds != 30*86400 {
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
			l.Remaining == nil || *l.Remaining != 38 || l.UsedPercent == nil || *l.UsedPercent != 24 || l.Unit != "requests" || l.WindowSeconds != 86400 {
			t.Errorf("free_model_daily_requests = %+v", l)
		}
		if _, ok := q.Quota.Raw["data"]; !ok {
			t.Errorf("raw = %v, want data object", q.Quota.Raw)
		}
	})

	t.Run("deepseek", func(t *testing.T) {
		var q quotaReply
		if code := manage(t, base, "GET", "/accounts/deepseek/quota", nil, &q); code != http.StatusOK {
			t.Fatalf("status = %d (%+v), want 200", code, q.Error)
		}
		if dsCalls.Load() != 1 {
			t.Fatalf("deepseek /user/balance calls = %d, want 1", dsCalls.Load())
		}
		if q.Quota.Plan != "available" {
			t.Errorf("plan = %q, want %q", q.Quota.Plan, "available")
		}
		if len(q.Quota.Limits) != 2 {
			t.Fatalf("limits = %+v, want 2 balances", q.Quota.Limits)
		}
		cny, usd := q.Quota.Limits[0], q.Quota.Limits[1]
		if cny.ID != "balance_cny" || cny.Label != "Balance (CNY)" || cny.Remaining == nil || *cny.Remaining != 110 || cny.Unit != "cny" {
			t.Errorf("cny limit = %+v", cny)
		}
		if usd.ID != "balance_usd" || usd.Label != "Balance (USD)" || usd.Remaining == nil || *usd.Remaining != 25 || usd.Unit != "usd" {
			t.Errorf("usd limit = %+v", usd)
		}
		if q.Quota.Raw["is_available"] != true {
			t.Errorf("raw = %v, want is_available true", q.Quota.Raw)
		}
	})

	t.Run("none", func(t *testing.T) {
		var q quotaReply
		if code := manage(t, base, "GET", "/accounts/none/quota", nil, &q); code != http.StatusUnprocessableEntity || q.Error == nil || q.Error.Code != "unsupported" {
			t.Fatalf("status = %d, error = %+v, want 422 unsupported", code, q.Error)
		}
		if noneCalls.Load() != 0 {
			t.Errorf("none account made %d non-models upstream calls, want 0", noneCalls.Load())
		}
	})
}
