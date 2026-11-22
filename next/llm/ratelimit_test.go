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

	// Cross-format filtering drops foreign headers.
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
