package llm

import (
	"net/http"
	"strings"
	"testing"
)

func TestExtractAndFilterRateLimitHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Requests-Limit", " 50 ")
	h.Set("Anthropic-Priority-Input-Tokens-Remaining", "12000")
	h.Set("Anthropic-Fast-Output-Tokens-Reset", "2026-11-22T08:00:00Z")
	h.Set("X-Ratelimit-Limit-Requests", "60")
	h.Set("X-Ratelimit-Limit", "200")
	h.Set("Set-Cookie", "sid=secret")
	h.Set("Anthropic-Organization-Id", "org_123")
	h.Set("Openai-Organization", "org-456")
	h.Set("Authorization", "Bearer tok")

	gotAnt := ExtractRateLimitHeaders(FormatAnthropicMessages, h)
	if len(gotAnt) != 3 ||
		gotAnt["anthropic-ratelimit-requests-limit"] != "50" ||
		gotAnt["anthropic-priority-input-tokens-remaining"] != "12000" ||
		gotAnt["anthropic-fast-output-tokens-reset"] != "2026-11-22T08:00:00Z" {
		t.Fatalf("anthropic extracted = %+v", gotAnt)
	}

	gotOAI := ExtractRateLimitHeaders(FormatOpenAIChat, h)
	if len(gotOAI) != 2 ||
		gotOAI["x-ratelimit-limit-requests"] != "60" ||
		gotOAI["x-ratelimit-limit"] != "200" {
		t.Fatalf("openai chat extracted = %+v", gotOAI)
	}

	gotResp := ExtractRateLimitHeaders(FormatOpenAIResponses, h)
	if len(gotResp) != 2 || gotResp["x-ratelimit-limit-requests"] != "60" {
		t.Fatalf("openai responses extracted = %+v", gotResp)
	}

	if gotGem := ExtractRateLimitHeaders(FormatGeminiGenerate, h); gotGem != nil {
		t.Fatalf("gemini extracted = %+v, want nil", gotGem)
	}

	if filtered := FilterRateLimitHeaders(FormatOpenAIChat, gotAnt); filtered != nil {
		t.Errorf("chat filter kept anthropic headers: %+v", filtered)
	}
	if filtered := FilterRateLimitHeaders(FormatAnthropicMessages, gotOAI); filtered != nil {
		t.Errorf("anthropic filter kept openai headers: %+v", filtered)
	}
	if filtered := FilterRateLimitHeaders(FormatGeminiGenerate, gotOAI); filtered != nil {
		t.Errorf("gemini filter kept openai headers: %+v", filtered)
	}
}

func TestValidRateLimitHeaders(t *testing.T) {
	if err := ValidRateLimitHeaders(map[string]string{"x-ratelimit-limit-requests": "60"}); err != nil {
		t.Fatalf("valid header failed: %v", err)
	}
	if err := ValidRateLimitHeaders(map[string]string{"Set-Cookie": "a=b"}); err == nil {
		t.Errorf("Set-Cookie accepted, want error")
	}
	if err := ValidRateLimitHeaders(map[string]string{"X-Ratelimit-Limit-Requests": "60"}); err == nil {
		t.Errorf("uppercase header name accepted, want error")
	}
	if err := ValidRateLimitHeaders(map[string]string{"x-ratelimit-limit-requests": "60\r\nEvil: 1"}); err == nil {
		t.Errorf("CRLF value accepted, want error")
	}
	if err := ValidRateLimitHeaders(map[string]string{"x-ratelimit-limit-requests": strings.Repeat("a", MaxRateLimitHeaderValue+1)}); err == nil {
		t.Errorf("oversized value accepted, want error")
	}
}

func TestValidRateLimitHeaderValueShapes(t *testing.T) {
	for _, v := range []string{
		"-1", "0", "60", "150000", "1795248000000", "18446744073709551615",
		"2026-11-22T08:00:00Z", "2026-11-22T08:00:00.123456789Z", "2026-11-22T18:00:00+10:00",
		"1s", "6m0s", "17ms", "1h2m3.5s", "0s", "59.5s", "1h0m0s", "1.5ms", "250us", "250µs", "100ns", "5m", "2h",
	} {
		if !ValidRateLimitHeaderValue(v) {
			t.Errorf("%q rejected, want accepted", v)
		}
	}
	for _, v := range []string{
		"", " ", "org_sweep_ID_123", "sk-sweepSecret", "org-456", "acc_1a2b3c4d", "a1b2c3d4e5f6a7",
		"-2", "-0", "-10", "--1", "-1.0", "- 1", "-1s", "+1", "1.5", "1e3", "0x10", "1,000", "60 requests", " 60", "60 ", "60\r\nEvil: 1", "60\n",
		"123456789012345678901", "ms", "s", "1x", "1s6m", "6m0s6m0s", "1h1h", "-1s", "1ms1s", ".5s",
		"2026-11-22", "2026-11-22 08:00:00Z", "2026-11-22T08:00:00", "2026-11-22t08:00:00z", "2026-11-22T08:00:00Z extra",
		"2026-11-22T08:00:00+1000", strings.Repeat("1", MaxRateLimitHeaderValue+1), strings.Repeat("a", 30),
	} {
		if ValidRateLimitHeaderValue(v) {
			t.Errorf("%q accepted, want rejected", v)
		}
	}
}

func TestRateLimitHeadersDropValuesOfOtherShapes(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "org_sweep_ID_123")
	h.Set("x-ratelimit-limit-tokens", "sk-sweepSecret")
	h.Set("x-ratelimit-remaining-requests", "59")
	h.Set("x-ratelimit-reset-requests", "6m0s")
	got := ExtractRateLimitHeaders(FormatOpenAIChat, h)
	if len(got) != 2 || got["x-ratelimit-remaining-requests"] != "59" || got["x-ratelimit-reset-requests"] != "6m0s" {
		t.Errorf("extracted = %+v, want only the count and the duration", got)
	}
	filtered := FilterRateLimitHeaders(FormatOpenAIChat, map[string]string{
		"x-ratelimit-limit-requests": "org_sweep_ID_123", "x-ratelimit-remaining-requests": " 59 ",
	})
	if len(filtered) != 1 || filtered["x-ratelimit-remaining-requests"] != "59" {
		t.Errorf("filtered = %+v, want only the count", filtered)
	}
	if err := ValidRateLimitHeaders(map[string]string{"x-ratelimit-limit-requests": "org_sweep_ID_123"}); err == nil {
		t.Errorf("an ID value passed ValidRateLimitHeaders")
	}
}

func TestPlainRateLimitValue(t *testing.T) {
	for _, v := range []string{
		"2026-11-22T08:00:00Z", "2026-11-22T18:00:00+10:00",
		"1s", "6m0s", "17ms", "1h2m3.5s", "0s", "59.5s", "1h0m0s", "1.5ms", "250us", "100ns", "5m", "2h", "9999h9999m9999.999s",
	} {
		if !PlainRateLimitValue(v) {
			t.Errorf("%q not plain, want plain", v)
		}
	}
	for _, v := range []string{
		"", "60", "-1", "4829175306", "org_sweep_ID_123",
		"4829175306s", "12345s", "1h2m12345s", "12345ms", "1.5555s", "1.5555ms", "1h2m3.1234s", "6m0s ",
		"2026-11-22T08:00:00.5Z", "2026-11-22T08:00:00.4829175306Z", "2026-11-22T08:00:00", "2026-11-22",
		strings.Repeat("1", 41) + "s",
	} {
		if PlainRateLimitValue(v) {
			t.Errorf("%q plain, want not plain", v)
		}
	}
}
