package llm

import (
	"fmt"
	"net/http"
	"strings"
)

// Bounds on upstream rate-limit headers carried over the extension protocol (minor version 34).
const (
	MaxRateLimitHeaders     = 32
	MaxRateLimitHeaderValue = 256
)

// Documented rate-limit response header names (lower-case) per wire format.
// Only headers explicitly documented by each vendor are listed; never cookies,
// auth tokens, or account/organization/workspace identifiers.
var (
	// Anthropic Messages: https://platform.claude.com/docs/en/api/rate-limits
	// and https://platform.claude.com/docs/en/build-with-claude/fast-mode.
	anthropicRateLimitHeaders = []string{
		"anthropic-ratelimit-requests-limit",
		"anthropic-ratelimit-requests-remaining",
		"anthropic-ratelimit-requests-reset",
		"anthropic-ratelimit-tokens-limit",
		"anthropic-ratelimit-tokens-remaining",
		"anthropic-ratelimit-tokens-reset",
		"anthropic-ratelimit-input-tokens-limit",
		"anthropic-ratelimit-input-tokens-remaining",
		"anthropic-ratelimit-input-tokens-reset",
		"anthropic-ratelimit-output-tokens-limit",
		"anthropic-ratelimit-output-tokens-remaining",
		"anthropic-ratelimit-output-tokens-reset",
		"anthropic-priority-input-tokens-limit",
		"anthropic-priority-input-tokens-remaining",
		"anthropic-priority-input-tokens-reset",
		"anthropic-priority-output-tokens-limit",
		"anthropic-priority-output-tokens-remaining",
		"anthropic-priority-output-tokens-reset",
		"anthropic-fast-input-tokens-limit",
		"anthropic-fast-input-tokens-remaining",
		"anthropic-fast-input-tokens-reset",
		"anthropic-fast-output-tokens-limit",
		"anthropic-fast-output-tokens-remaining",
		"anthropic-fast-output-tokens-reset",
	}

	// OpenAI Chat Completions and Responses: https://developers.openai.com/api/docs/guides/rate-limits
	// plus OpenRouter 429 rate-limit headers: https://openrouter.ai/docs/api_reference/limits.
	openaiRateLimitHeaders = []string{
		"x-ratelimit-limit-requests",
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-reset-requests",
		"x-ratelimit-reset-tokens",
		"x-ratelimit-limit-project-tokens",
		"x-ratelimit-remaining-project-tokens",
		"x-ratelimit-reset-project-tokens",
		"x-ratelimit-limit",
		"x-ratelimit-remaining",
		"x-ratelimit-reset",
	}

	anthropicRateLimitSet = headerSet(anthropicRateLimitHeaders)
	openaiRateLimitSet    = headerSet(openaiRateLimitHeaders)
	allRateLimitSet       = headerSet(append(append([]string(nil), anthropicRateLimitHeaders...), openaiRateLimitHeaders...))
)

func headerSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// AllowedRateLimitHeader reports whether name (case-insensitive) is a documented rate-limit
// response header for format. Formats whose vendor documents no rate-limit headers (such as
// Gemini GenerateContent) return false for every header.
func AllowedRateLimitHeader(format, name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch format {
	case FormatAnthropicMessages:
		return anthropicRateLimitSet[n]
	case FormatOpenAIChat, FormatOpenAIResponses:
		return openaiRateLimitSet[n]
	default:
		return false
	}
}

// ValidRateLimitHeaderValue reports whether v is a safe single-line header value within
// MaxRateLimitHeaderValue bytes: non-empty, no control characters (including CR and LF).
func ValidRateLimitHeaderValue(v string) bool {
	if len(v) == 0 || len(v) > MaxRateLimitHeaderValue {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return false
		}
	}
	return true
}

// ExtractRateLimitHeaders reads the documented rate-limit headers for format from h, returning
// a lower-case map of valid trimmed values, or nil when none are present.
func ExtractRateLimitHeaders(format string, h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	var names []string
	switch format {
	case FormatAnthropicMessages:
		names = anthropicRateLimitHeaders
	case FormatOpenAIChat, FormatOpenAIResponses:
		names = openaiRateLimitHeaders
	default:
		return nil
	}
	var out map[string]string
	for _, name := range names {
		v := strings.TrimSpace(h.Get(name))
		if !ValidRateLimitHeaderValue(v) {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[name] = v
	}
	return out
}

// FilterRateLimitHeaders returns only the entries of headers that are documented rate-limit
// headers for format with valid trimmed values, keyed by lower-case name. Returns nil when none
// remain.
func FilterRateLimitHeaders(format string, headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	var out map[string]string
	for k, v := range headers {
		n := strings.ToLower(strings.TrimSpace(k))
		if !AllowedRateLimitHeader(format, n) {
			continue
		}
		v = strings.TrimSpace(v)
		if !ValidRateLimitHeaderValue(v) {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[n] = v
	}
	return out
}

// ValidRateLimitHeaders checks that headers satisfies the protocol 0.34 invariants: at most
// MaxRateLimitHeaders entries, every key is lower-case and in the explicit documented allowlist,
// and every value passes ValidRateLimitHeaderValue.
func ValidRateLimitHeaders(headers map[string]string) error {
	if len(headers) > MaxRateLimitHeaders {
		return fmt.Errorf("too many rate-limit headers: %d (max %d)", len(headers), MaxRateLimitHeaders)
	}
	for k, v := range headers {
		if k != strings.ToLower(k) || !allRateLimitSet[k] {
			return fmt.Errorf("header %q is not an allowed lower-case rate-limit header", k)
		}
		if !ValidRateLimitHeaderValue(v) {
			return fmt.Errorf("header %q has invalid value", k)
		}
	}
	return nil
}
