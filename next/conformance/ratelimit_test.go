package conformance

import (
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformanceRateLimitHeaders(t *testing.T) {
	if protocol.Minor < host.RateLimitHeadersMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.RateLimitHeadersMinor)
	}

	valid := []llm.Event{
		{
			Type: llm.EventResponseStart,
			Response: &llm.ResponseInfo{
				ID:      "r1",
				Model:   "m",
				Format:  llm.FormatOpenAIChat,
				Headers: map[string]string{"x-ratelimit-limit-requests": "60"},
			},
		},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	if err := checkReplyRateLimitHeaders(valid); err != nil {
		t.Fatalf("valid response headers failed: %v", err)
	}

	validErr := []llm.Event{
		{
			Type: llm.EventError,
			Error: &llm.Error{
				Code:    llm.CodeRateLimit,
				Message: "limited",
				Headers: map[string]string{"x-ratelimit-limit": "200"},
			},
		},
	}
	if err := checkReplyRateLimitHeaders(validErr); err != nil {
		t.Fatalf("valid error headers failed: %v", err)
	}

	invalidCases := []struct {
		name string
		evs  []llm.Event
	}{
		{
			name: "non-allowlisted cookie header on response.start",
			evs: []llm.Event{{
				Type:     llm.EventResponseStart,
				Response: &llm.ResponseInfo{ID: "r1", Headers: map[string]string{"set-cookie": "a=b"}},
			}},
		},
		{
			name: "org id header on error",
			evs: []llm.Event{{
				Type:  llm.EventError,
				Error: &llm.Error{Code: llm.CodeRateLimit, Headers: map[string]string{"anthropic-organization-id": "org_1"}},
			}},
		},
		{
			name: "response.headers on finish",
			evs: []llm.Event{{
				Type:     llm.EventFinish,
				Response: &llm.ResponseInfo{Headers: map[string]string{"x-ratelimit-limit-requests": "60"}},
			}},
		},
		{
			name: "error.headers on finish",
			evs: []llm.Event{{
				Type:  llm.EventFinish,
				Error: &llm.Error{Headers: map[string]string{"x-ratelimit-limit": "200"}},
			}},
		},
		{
			name: "CRLF injection in header value",
			evs: []llm.Event{{
				Type:     llm.EventResponseStart,
				Response: &llm.ResponseInfo{ID: "r1", Headers: map[string]string{"x-ratelimit-limit-requests": "60\r\nX-Evil: 1"}},
			}},
		},
	}
	for _, tc := range invalidCases {
		if err := checkReplyRateLimitHeaders(tc.evs); err == nil {
			t.Errorf("%s: checkReplyRateLimitHeaders returned nil, want error", tc.name)
		}
	}
}
