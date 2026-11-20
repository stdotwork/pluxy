package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Gemini offers no quota endpoint, so GET /accounts/{id}/quota answers with what the account's
// last HTTP 429 reply named (QuotaFailure limit, RetryInfo reset, ErrorInfo reason) and when it
// was seen, and reports empty limits before any 429 has been seen.
func TestGeminiQuotaFromLast429(t *testing.T) {
	const apiKey = "AIza-e2e-gemini-quota-key"
	upstream := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1beta/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{
			"error": {
				"code": 429,
				"message": "Quota exceeded for key `+apiKey+`",
				"status": "RESOURCE_EXHAUSTED",
				"details": [
					{
						"@type": "type.googleapis.com/google.rpc.ErrorInfo",
						"reason": "RATE_LIMIT_EXCEEDED",
						"domain": "generativelanguage.googleapis.com",
						"metadata": {"model": "gemini-2.5-flash"}
					},
					{
						"@type": "type.googleapis.com/google.rpc.QuotaFailure",
						"violations": [
							{
								"subject": "key:`+apiKey+`",
								"description": "GenerateContent requests per minute per model",
								"apiService": "generativelanguage.googleapis.com",
								"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests",
								"quotaId": "GenerateRequestsPerMinutePerProjectPerModel-FreeTier",
								"quotaDimensions": {"location": "global", "model": "gemini-2.5-flash"},
								"quotaValue": "15"
							}
						]
					},
					{
						"@type": "type.googleapis.com/google.rpc.RetryInfo",
						"retryDelay": "45s"
					}
				]
			}
		}`)
	})

	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{
				map[string]any{
					"id":         "g-quota",
					"settings":   map[string]any{"base_url": upstream.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": apiKey},
				},
			},
		},
	})

	type quotaReply struct {
		Quota struct {
			Plan   string `json:"plan"`
			Limits []struct {
				ID            string   `json:"id"`
				Label         string   `json:"label"`
				Limit         *float64 `json:"limit"`
				Unit          string   `json:"unit"`
				ResetsAt      int64    `json:"resets_at"`
				WindowSeconds int64    `json:"window_seconds"`
				Models        []string `json:"models"`
			} `json:"limits"`
			Raw map[string]any `json:"raw"`
		} `json:"quota"`
		FetchedAt time.Time                       `json:"fetched_at"`
		Error     *struct{ Code, Message string } `json:"error"`
	}

	var empty quotaReply
	if code := manage(t, base, "GET", "/accounts/g-quota/quota", nil, &empty); code != http.StatusOK {
		t.Fatalf("initial quota status %d (%+v), want 200", code, empty.Error)
	}
	if len(empty.Quota.Limits) != 0 {
		t.Fatalf("initial quota limits = %+v, want empty before any 429", empty.Quota.Limits)
	}
	if !strings.Contains(strings.ToLower(empty.Quota.Plan), "no rate-limit") {
		t.Errorf("initial plan = %q, want note that no rate-limit reply has been seen yet", empty.Quota.Plan)
	}

	before := time.Now().UTC()
	resp := postPath(t, context.Background(), base, "/v1beta/models/gemini-2.5-flash:generateContent", clientKey,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("generateContent status = %d, want 429", resp.StatusCode)
	}
	after := time.Now().UTC()

	var after429 quotaReply
	if code := manage(t, base, "GET", "/accounts/g-quota/quota", nil, &after429); code != http.StatusOK {
		t.Fatalf("quota after 429 status %d (%+v), want 200", code, after429.Error)
	}
	if !strings.Contains(strings.ToLower(after429.Quota.Plan), "last-seen") {
		t.Errorf("plan = %q, want last-seen caveat", after429.Quota.Plan)
	}
	if len(after429.Quota.Limits) != 1 {
		t.Fatalf("limits = %+v, want 1 limit from QuotaFailure", after429.Quota.Limits)
	}
	l := after429.Quota.Limits[0]
	if l.ID != "GenerateRequestsPerMinutePerProjectPerModel-FreeTier" {
		t.Errorf("limit id = %q", l.ID)
	}
	if !strings.Contains(strings.ToLower(l.Label), "last seen") || !strings.Contains(strings.ToLower(l.Label), "not a live reading") {
		t.Errorf("limit label = %q, want last-seen / not a live reading caveat", l.Label)
	}
	if l.Limit == nil || *l.Limit != 15 || l.Unit != "requests" {
		t.Errorf("limit = %v %q, want 15 requests", l.Limit, l.Unit)
	}
	if l.WindowSeconds != 60 {
		t.Errorf("window_seconds = %d, want 60", l.WindowSeconds)
	}
	if l.ResetsAt < before.Unix()+45 || l.ResetsAt > after.Unix()+46 {
		t.Errorf("resets_at = %d, want within [%d, %d]", l.ResetsAt, before.Unix()+45, after.Unix()+46)
	}
	if len(l.Models) != 1 || l.Models[0] != "gemini-2.5-flash" {
		t.Errorf("models = %v, want [gemini-2.5-flash]", l.Models)
	}
	if rawJSON, _ := json.Marshal(after429.Quota); strings.Contains(string(rawJSON), apiKey) {
		t.Errorf("quota reply leaked account API key: %s", rawJSON)
	}
	if _, ok := after429.Quota.Raw["observed_at"]; !ok {
		t.Errorf("raw = %v, want observed_at timestamp", after429.Quota.Raw)
	}
}
