package main

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// The cases and their expected outcomes are today's core's (core/internal/runtime/executor:
// claude_executor_fable_ratelimit_test.go, helps/claude_ratelimit_test.go and
// helps/claude_request_rate_test.go), without its random 1-30 s grace.
func TestRateLimit(t *testing.T) {
	now := time.Unix(1793000000, 0)
	at := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	const (
		quota = llm.CodeQuota
		limit = llm.CodeRateLimit
		cred  = llm.ScopeCredential
		model = llm.ScopeModel
		week  = 7 * 24 * time.Hour
	)
	perMinute := `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`
	plain := `{"type":"error","error":{"type":"rate_limit_error","message":"Shared usage window rejected."}}`
	issue5915 := map[string]string{
		hUnifiedStatus: "rejected", hRepresentative: "seven_day_overage_included",
		h7dStatus: "allowed", h7dUtilization: "0.69", h5hUtilization: "0.00",
		h7dOIStatus: "rejected", "Anthropic-Ratelimit-Unified-7d_oi-Utilization": "1.02",
		hOverageStatus: "rejected", hOverageReason: "org_spend_cap_reached", "Retry-After": "121180",
	}
	for _, c := range []struct {
		name    string
		headers map[string]string
		body    string
		cooling bool
		code    string
		scope   string
		wait    time.Duration
		window  int64
	}{
		{name: "model's own window refused", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "allowed", h7dStatus: "allowed", h7dOIStatus: "rejected", "Retry-After": "120"},
			code: limit, scope: model},
		{name: "model's own window refused, 7d allowed_warning", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "allowed", h7dStatus: "allowed_warning", h7dOIStatus: "rejected", "Retry-After": "120"},
			code: limit, scope: model},
		{name: "model's own window refused, 5h allowed_warning", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "allowed_warning", h7dStatus: "allowed", h7dOIStatus: "rejected"},
			code: limit, scope: model},
		{name: "model's own window refused, with its reset", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "allowed", h7dStatus: "allowed", h7dOIStatus: "rejected", h7dOIReset: at(week), hUnifiedReset: at(week), "Retry-After": "60"},
			code: limit, scope: model},
		{name: "overage spend cap, issue 5915", headers: issue5915, code: limit, scope: model},
		{name: "5h refused", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", "Retry-After": "60"}, body: `{"type":"error","error":{"type":"rate_limit_error"}}`,
			code: quota, scope: cred, wait: time.Minute},
		{name: "5h refused, model-level cooling", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", "Retry-After": "60"}, body: `{"type":"error","error":{"type":"rate_limit_error"}}`, cooling: true,
			code: quota, scope: model, wait: time.Minute},
		{name: "5h refused, 7d allowed", headers: map[string]string{h5hStatus: "rejected", h7dStatus: "allowed"}, code: quota, scope: cred},
		{name: "7d refused, 5h allowed", headers: map[string]string{h5hStatus: "allowed", h7dStatus: "rejected"}, code: quota, scope: cred},
		{name: "5h refused with 7d_oi refused", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", h7dStatus: "allowed_warning", h7dOIStatus: "rejected"}, code: quota, scope: cred},
		{name: "aggregate refusal alone", headers: map[string]string{hUnifiedStatus: "rejected"}, code: quota, scope: cred},
		{name: "aggregate refusal, statuses malformed", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "unknown", h7dStatus: "invalid"}, code: quota, scope: cred},
		{name: "Retry-After seconds", headers: map[string]string{"Retry-After": "60"}, code: limit, scope: model, wait: time.Minute},
		{name: "Retry-After date", headers: map[string]string{"Retry-After": now.Add(90 * time.Second).UTC().Format(http.TimeFormat)}, code: limit, scope: model, wait: 90 * time.Second},
		{name: "5h reset is the wait", headers: map[string]string{h5hStatus: "rejected", h5hReset: at(5 * time.Hour), h7dStatus: "allowed", h7dReset: at(week), hUnifiedReset: at(5 * time.Hour)},
			code: quota, scope: cred, wait: 5 * time.Hour, window: 300},
		{name: "7d reset is the wait", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "allowed", h5hReset: at(5 * time.Hour), h7dStatus: "rejected", h7dReset: at(week)},
			code: quota, scope: cred, wait: week, window: 10080},
		{name: "both refused: the later reset", headers: map[string]string{h5hStatus: "rejected", h5hReset: at(5 * time.Hour), h7dStatus: "rejected", h7dReset: at(week)},
			code: quota, scope: cred, wait: week, window: 10080},
		{name: "all allowed", headers: map[string]string{hUnifiedStatus: "allowed", h5hStatus: "allowed", h5hReset: at(5 * time.Hour), h7dStatus: "allowed", h7dReset: at(week)},
			code: limit, scope: model},
		{name: "no aggregate status, shared windows allowed_warning: Retry-After, not the aggregate reset", headers: map[string]string{h5hStatus: "allowed_warning", h7dStatus: "allowed_warning", hUnifiedReset: at(week), "Retry-After": "60"},
			code: limit, scope: model, wait: time.Minute},
		{name: "5h refused with 7d_oi refused: the later 7d_oi reset", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", h5hReset: at(5 * time.Hour), h7dStatus: "allowed", h7dOIStatus: "rejected", h7dOIReset: at(week)},
			code: quota, scope: cred, wait: week},
		{name: "reset in the past", headers: map[string]string{h5hStatus: "rejected", h5hReset: at(-5 * time.Hour)}, code: quota, scope: cred},
		{name: "overage claim without shared windows, issue 6465", headers: map[string]string{hUnifiedStatus: "rejected", hRepresentative: "overage", hUnifiedReset: at(48 * time.Hour), hOverageStatus: "allowed", hOverageReset: at(48 * time.Hour), "Anthropic-Ratelimit-Unified-Overage-Utilization": "0.11"},
			code: limit, scope: model},
		{name: "refused 5h ignores a later aggregate reset", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", h5hReset: at(5 * time.Hour), hUnifiedReset: at(30 * 24 * time.Hour), hRepresentative: "five_hour"},
			code: quota, scope: cred, wait: 5 * time.Hour, window: 300},
		{name: "aggregate reset that is overage's billing reset", headers: map[string]string{hUnifiedStatus: "rejected", hRepresentative: "overage", h7dStatus: "allowed", h7dUtilization: "0.69", h5hUtilization: "1.0", hUnifiedReset: at(48 * time.Hour), hOverageStatus: "rejected", hOverageReset: at(48 * time.Hour), "Retry-After": "60"},
			code: quota, scope: cred, wait: time.Minute},
		{name: "five_hour claim with overage disabled", headers: map[string]string{hUnifiedStatus: "rejected", hRepresentative: "five_hour", hOverageReason: "org_level_disabled", "Retry-After": "10800"},
			code: quota, scope: cred, wait: 3 * time.Hour},
		{name: "overage claim with 5h used up", headers: map[string]string{hUnifiedStatus: "rejected", hRepresentative: "overage", h5hUtilization: "1.0", "Retry-After": "10800"},
			code: quota, scope: cred, wait: 3 * time.Hour},
		{name: "overage claim with 5h room", headers: map[string]string{hUnifiedStatus: "rejected", hRepresentative: "overage", h5hUtilization: "0.00", hUnifiedReset: at(48 * time.Hour), hOverageReset: at(48 * time.Hour), "Retry-After": "10800"},
			code: limit, scope: model},
		{name: "Retry-After beyond 7 days and an hour", headers: map[string]string{hUnifiedStatus: "rejected", h5hStatus: "rejected", "Retry-After": "2034192"}, code: quota, scope: cred},
		{name: "allowed overage claim keeps a short Retry-After", headers: map[string]string{hRepresentative: "overage", hOverageStatus: "allowed", hUnifiedStatus: "allowed", h7dStatus: "allowed_warning", "Retry-After": "30"},
			code: limit, scope: model, wait: 30 * time.Second},
		{name: "overage claim, no statuses, keeps a short Retry-After", headers: map[string]string{hRepresentative: "overage", hOverageStatus: "allowed", "Retry-After": "30"},
			code: limit, scope: model, wait: 30 * time.Second},
		{name: "model's own window, 5h left out, issue 7197", headers: map[string]string{"Retry-After": "546534", hUnifiedStatus: "rejected", hRepresentative: "seven_day_overage_included", h7dStatus: "allowed", h7dUtilization: "0.62", h7dReset: at(6 * 24 * time.Hour), h7dOIStatus: "rejected", h7dOIReset: at(6 * 24 * time.Hour), hOverageStatus: "rejected", hOverageReason: "out_of_credits", hUnifiedReset: at(6 * 24 * time.Hour)},
			code: limit, scope: model},
		{name: "per-minute limit: Retry-After, not the aggregate reset", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour)}, body: perMinute,
			code: limit, scope: model, wait: 30 * time.Second},
		{name: "per-minute limit, other letter case", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour)},
			body: `{"type":"error","error":{"type":"rate_limit_error","message":"  THIS REQUEST WOULD EXCEED THE OUTPUT TOKENS PER MINUTE RATE LIMIT. PLEASE TRY AGAIN LATER.\n"}}`,
			code: limit, scope: model, wait: 30 * time.Second},
		{name: "other message: the aggregate reset", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour)}, body: plain,
			code: limit, scope: model, wait: 4 * time.Hour},
		{name: "per-minute message, wrong type", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour)},
			body: `{"type":"error","error":{"type":"api_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`,
			code: limit, scope: model, wait: 4 * time.Hour},
		{name: "per-minute message, zero Retry-After", headers: map[string]string{"Retry-After": "0", hUnifiedReset: at(4 * time.Hour)}, body: perMinute,
			code: limit, scope: model, wait: 4 * time.Hour},
		{name: "per-minute message, Retry-After too far", headers: map[string]string{"Retry-After": strconv.Itoa(int(maxLimitWait/time.Second) + 1), hUnifiedReset: at(4 * time.Hour)}, body: perMinute,
			code: limit, scope: model, wait: 4 * time.Hour},
		{name: "per-minute message with a quota status", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour), hUnifiedStatus: "rejected"}, body: perMinute,
			code: quota, scope: cred, wait: 4 * time.Hour},
		{name: "per-minute message, shared window allowed", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour), h5hStatus: "allowed"}, body: perMinute,
			code: limit, scope: model, wait: 30 * time.Second},
		{name: "per-minute message, overage only", headers: map[string]string{"Retry-After": "30", hUnifiedReset: at(4 * time.Hour), hUnifiedStatus: "rejected", h5hStatus: "allowed", h7dStatus: "allowed", hOverageStatus: "rejected"}, body: perMinute,
			code: limit, scope: model},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range c.headers {
				h.Set(k, v)
			}
			code, scope, wait, window := rateLimit(h, []byte(c.body), c.cooling, now)
			if code != c.code || scope != c.scope || wait != c.wait || window != c.window {
				t.Errorf("got %s %s wait %v window %d; want %s %s wait %v window %d", code, scope, wait, window, c.code, c.scope, c.wait, c.window)
			}
		})
	}
}

// From helps/claude_ratelimit_test.go: with the 7d window allowed and the 5h status left out, an
// overage refusal is the model's only while the 5h utilization is missing or below 1.
func TestRateLimitUtilization(t *testing.T) {
	for _, c := range []struct {
		name, value string
		set, shared bool
	}{
		{"zero", "0.00", true, false}, {"half", "0.50", true, false}, {"missing", "", false, false},
		{"empty", "", true, true}, {"spaces", " \t ", true, true}, {"text", "invalid", true, true},
		{"NaN", "NaN", true, true}, {"+Inf", "+Inf", true, true}, {"-Inf", "-Inf", true, true},
		{"negative", "-0.1", true, true}, {"one", "1.0", true, true}, {"over", "1.05", true, true},
	} {
		h := http.Header{}
		for k, v := range map[string]string{hUnifiedStatus: "rejected", hRepresentative: "seven_day_overage_included", h7dStatus: "allowed",
			h7dUtilization: "0.69", h7dOIStatus: "rejected", hOverageStatus: "rejected", hOverageReason: "org_spend_cap_reached"} {
			h.Set(k, v)
		}
		if c.set {
			h.Set(h5hUtilization, c.value)
		}
		code, scope, _, _ := rateLimit(h, nil, false, time.Now())
		if shared := code == llm.CodeQuota && scope == llm.ScopeCredential; shared != c.shared {
			t.Errorf("%s: got %s %s; want the account's quota %v", c.name, code, scope, c.shared)
		}
	}
}

// Two Retry-After values are not one usable wait, so a per-minute message keeps the aggregate
// reset.
func TestRateLimitRepeatedRetryAfter(t *testing.T) {
	now := time.Unix(1793000000, 0)
	h := http.Header{"Retry-After": {"30", "30"}, hUnifiedReset: {strconv.FormatInt(now.Add(4*time.Hour).Unix(), 10)}}
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`)
	if _, _, wait, _ := rateLimit(h, body, false, now); wait != 4*time.Hour {
		t.Errorf("wait %v, want 4h", wait)
	}
}
