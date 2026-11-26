package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Anthropic's rate-limit headers. A Claude subscription has a shared 5-hour window and a shared
// 7-day window that every model draws on, a 7-day window of its own for some models (7d_oi), and
// overage (paid use past the windows). The aggregate unified status is the one that refused the
// request, the representative claim names which.
const (
	hUnifiedStatus  = "Anthropic-Ratelimit-Unified-Status"
	hUnifiedReset   = "Anthropic-Ratelimit-Unified-Reset"
	h5hStatus       = "Anthropic-Ratelimit-Unified-5h-Status"
	h5hReset        = "Anthropic-Ratelimit-Unified-5h-Reset"
	h5hUtilization  = "Anthropic-Ratelimit-Unified-5h-Utilization"
	h7dStatus       = "Anthropic-Ratelimit-Unified-7d-Status"
	h7dReset        = "Anthropic-Ratelimit-Unified-7d-Reset"
	h7dUtilization  = "Anthropic-Ratelimit-Unified-7d-Utilization"
	h7dOIStatus     = "Anthropic-Ratelimit-Unified-7d_oi-Status"
	h7dOIReset      = "Anthropic-Ratelimit-Unified-7d_oi-Reset"
	hOverageStatus  = "Anthropic-Ratelimit-Unified-Overage-Status"
	hOverageReset   = "Anthropic-Ratelimit-Unified-Overage-Reset"
	hOverageReason  = "Anthropic-Ratelimit-Unified-Overage-Disabled-Reason"
	hRepresentative = "Anthropic-Ratelimit-Unified-Representative-Claim"
)

// maxLimitWait is the longest window Anthropic has (7 days) plus an hour for clock skew. A later
// reset is a billing boundary, not a rate limit to wait out.
const maxLimitWait = 7*24*time.Hour + time.Hour

// rateLimit classifies a 429 as today's core does. A refused shared 5-hour or 7-day window (or an
// aggregate refusal that is not only a model's own window or overage) is the account's quota,
// which no model can use until the reset; with modelLevelCooling it is kept on the model alone.
// A refusal of only a model's own window or of overage, and a plain 429, keep the model out on
// this account and leave its other models free. wait is how long the upstream said to wait
// (0 when it did not say), and window the length in minutes of the shared window whose reset
// that is (300 or 10080; 0 for neither).
func rateLimit(h http.Header, body []byte, modelLevelCooling bool, now time.Time) (code, scope string, wait time.Duration, window int64) {
	unified, s5h, s7d, s7dOI := limitStatus(h, hUnifiedStatus), limitStatus(h, h5hStatus), limitStatus(h, h7dStatus), limitStatus(h, h7dOIStatus)
	modelOnly := overageOrModelOnly(h, unified, s5h, s7d, s7dOI)
	code, scope = llm.CodeRateLimit, llm.ScopeModel
	if s5h == "rejected" || s7d == "rejected" || (unified == "rejected" && !modelOnly) {
		code = llm.CodeQuota
		if !modelLevelCooling {
			scope = llm.ScopeCredential
		}
	}

	// The wait is the latest reset among the refused windows (and Retry-After), as long as it
	// is in the future and within maxLimitWait.
	var latest, reset5h, reset7d time.Time
	consider := func(t time.Time, ok bool) time.Time {
		if !ok || !t.After(now) || t.Sub(now) > maxLimitWait {
			return time.Time{}
		}
		if t.After(latest) {
			latest = t
		}
		return t
	}
	if !modelOnly {
		// A refusal of only a model's window or overage says nothing about the account, so its
		// Retry-After (often days) is not waited out.
		consider(parseRetryAfter(h.Get("Retry-After"), now))
	}
	if s5h == "rejected" {
		reset5h = consider(parseReset(h.Get(h5hReset)))
	}
	if s7d == "rejected" {
		reset7d = consider(parseReset(h.Get(h7dReset)))
	}
	if s7dOI == "rejected" && !modelOnly {
		consider(parseReset(h.Get(h7dOIReset)))
	}
	unifiedApplies := !modelOnly && (unified == "rejected" || s5h == "rejected" || s7d == "rejected" || s7dOI == "rejected" ||
		(unified == "" && !windowAllowed(s5h) && !windowAllowed(s7d)))
	if raw := h.Get(hUnifiedReset); unifiedApplies && raw != "" && !perMinuteLimit(h, body, now) && !billingBoundary(h, raw) {
		consider(parseReset(raw))
	}
	if latest.IsZero() {
		return code, scope, 0, 0
	}
	switch {
	case latest.Equal(reset7d):
		window = 7 * 24 * 60
	case latest.Equal(reset5h):
		window = 5 * 60
	}
	return code, scope, latest.Sub(now), window
}

// limitStatus reads a rate-limit status header: lower case, without surrounding spaces.
func limitStatus(h http.Header, name string) string {
	return strings.ToLower(strings.TrimSpace(h.Get(name)))
}

func windowAllowed(s string) bool { return s == "allowed" || s == "allowed_warning" }

// overageOrModelOnly reports whether a refusal is of only overage or a model's own 7-day window
// while the shared windows still have room: allowed, or left out of the reply where Anthropic
// leaves them out when it did not evaluate them.
func overageOrModelOnly(h http.Header, unified, s5h, s7d, s7dOI string) bool {
	if s5h == "rejected" || s7d == "rejected" {
		return false
	}
	claim := limitStatus(h, hRepresentative)
	if s7dOI != "rejected" && limitStatus(h, hOverageStatus) != "rejected" && strings.TrimSpace(h.Get(hOverageReason)) == "" && !strings.Contains(claim, "overage") {
		return false
	}
	ok5h, ok7d := windowAllowed(s5h), windowAllowed(s7d)
	switch {
	case ok5h && ok7d:
		return true
	case ok7d && s5h == "" && (utilizationHealthy(h.Get(h5hUtilization)) || (unified == "rejected" && len(h.Values(h5hUtilization)) == 0)):
		// The 5-hour status is left out when that window has no use or the plan has none.
		return true
	case ok5h && s7d == "" && utilizationHealthy(h.Get(h7dUtilization)):
		return true
	}
	// Both shared statuses left out: only an overage claim may use that, and only while the
	// shared utilizations given (if any) are below 1.
	if unified == "rejected" && s5h == "" && s7d == "" && strings.Contains(claim, "overage") {
		u5, u7 := h.Get(h5hUtilization), h.Get(h7dUtilization)
		return (strings.TrimSpace(u5) == "" || utilizationHealthy(u5)) && (strings.TrimSpace(u7) == "" || utilizationHealthy(u7))
	}
	return false
}

// utilizationHealthy reports whether a utilization header holds a number from 0 up to, not
// including, 1.
func utilizationHealthy(raw string) bool {
	u, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && !math.IsNaN(u) && !math.IsInf(u, 0) && u >= 0 && u < 1
}

// perMinuteLimit reports a refusal of one of the per-minute limits, which has no window: the
// body is exactly one of Anthropic's per-minute messages (letter case and surrounding spaces
// aside), no quota status header has a value, and there is one Retry-After within the limits.
// Then the aggregate reset, which may be hours away, is not waited for.
func perMinuteLimit(h http.Header, body []byte, now time.Time) bool {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Type != "rate_limit_error" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Error.Message)) {
	case "this request would exceed the requests per minute rate limit. please try again later.",
		"this request would exceed the input tokens per minute rate limit. please try again later.",
		"this request would exceed the output tokens per minute rate limit. please try again later.":
	default:
		return false
	}
	for _, name := range []string{hUnifiedStatus, h5hStatus, h7dStatus, h7dOIStatus, hOverageStatus} {
		for _, v := range h.Values(name) {
			if strings.TrimSpace(v) != "" {
				return false
			}
		}
	}
	ra := h.Values("Retry-After")
	if len(ra) != 1 {
		return false
	}
	t, ok := parseRetryAfter(ra[0], now)
	return ok && t.After(now) && t.Sub(now) <= maxLimitWait
}

// billingBoundary reports whether the aggregate reset is overage's billing-period reset: the
// claim names overage and the two resets are the same time.
func billingBoundary(h http.Header, unifiedReset string) bool {
	if !strings.Contains(limitStatus(h, hRepresentative), "overage") {
		return false
	}
	u, ok1 := parseReset(unifiedReset)
	o, ok2 := parseReset(h.Get(hOverageReset))
	return ok1 && ok2 && u.Equal(o)
}

// parseReset reads a reset header: Unix seconds (fractions allowed), RFC 3339 or an HTTP date.
func parseReset(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil {
		if sec <= 0 || sec > math.MaxInt64/2 {
			return time.Time{}, false
		}
		whole := math.Floor(sec)
		return time.Unix(int64(whole), int64((sec-whole)*1e9)), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseRetryAfter reads Retry-After: seconds (fractions allowed), an HTTP date or RFC 3339.
func parseRetryAfter(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil {
		if sec <= 0 || sec > maxLimitWait.Seconds()*2 {
			return time.Time{}, false
		}
		return now.Add(time.Duration(sec * float64(time.Second))), true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}
