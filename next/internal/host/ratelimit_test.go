package host

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestRateLimitHeadersMinorGating(t *testing.T) {
	if RateLimitHeadersMinor != 34 {
		t.Fatalf("RateLimitHeadersMinor = %d, want 34", RateLimitHeadersMinor)
	}
	if protocol.Minor < RateLimitHeadersMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, RateLimitHeadersMinor)
	}
	start := llm.Event{
		Type: llm.EventResponseStart,
		Response: &llm.ResponseInfo{
			ID:      "r1",
			Model:   "m",
			Format:  llm.FormatOpenAIChat,
			Headers: map[string]string{"x-ratelimit-limit-requests": "60"},
		},
	}
	errEv := llm.Event{
		Type: llm.EventError,
		Error: &llm.Error{
			Code:    llm.CodeRateLimit,
			Message: "limited",
			Headers: map[string]string{"x-ratelimit-remaining": "0"},
		},
	}

	oldExt := &Extension{info: protocol.Info{ProtocolMinor: RateLimitHeadersMinor - 1}}
	gotOldStart := oldExt.normalizeEvent(start)
	if gotOldStart.Response.Headers != nil {
		t.Errorf("minor %d kept Response.Headers: %+v, want nil", RateLimitHeadersMinor-1, gotOldStart.Response.Headers)
	}
	gotOldErr := oldExt.normalizeEvent(errEv)
	if gotOldErr.Error.Headers != nil {
		t.Errorf("minor %d kept Error.Headers: %+v, want nil", RateLimitHeadersMinor-1, gotOldErr.Error.Headers)
	}

	newExt := &Extension{info: protocol.Info{ProtocolMinor: RateLimitHeadersMinor}}
	gotNewStart := newExt.normalizeEvent(start)
	if gotNewStart.Response.Headers["x-ratelimit-limit-requests"] != "60" {
		t.Errorf("minor %d Response.Headers = %+v, want preserved", RateLimitHeadersMinor, gotNewStart.Response.Headers)
	}
	gotNewErr := newExt.normalizeEvent(errEv)
	if gotNewErr.Error.Headers["x-ratelimit-remaining"] != "0" {
		t.Errorf("minor %d Error.Headers = %+v, want preserved", RateLimitHeadersMinor, gotNewErr.Error.Headers)
	}
}
