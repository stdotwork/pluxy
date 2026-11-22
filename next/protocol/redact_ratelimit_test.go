package protocol_test

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestRedactorEventRateLimitHeaders(t *testing.T) {
	secret := "sk-super-secret-token-1234567890"
	r := protocol.NewRedactor(&protocol.Credential{Kind: "api_key", APIKey: secret})

	origResp := llm.Event{
		Type: llm.EventResponseStart,
		Response: &llm.ResponseInfo{
			ID:     "r1",
			Model:  "m",
			Format: llm.FormatOpenAIChat,
			Headers: map[string]string{
				"x-ratelimit-limit-requests": "limit-" + secret,
			},
		},
	}
	redResp := r.Event(origResp)
	got := redResp.Response.Headers["x-ratelimit-limit-requests"]
	if strings.Contains(got, secret) || !strings.Contains(got, protocol.Redacted) {
		t.Errorf("response header not redacted: %q", got)
	}
	if !strings.Contains(origResp.Response.Headers["x-ratelimit-limit-requests"], secret) {
		t.Errorf("redacting response headers mutated caller map")
	}

	origErr := llm.Event{
		Type: llm.EventError,
		Error: &llm.Error{
			Code:    llm.CodeRateLimit,
			Message: "limited",
			Headers: map[string]string{
				"anthropic-ratelimit-requests-reset": "reset-" + secret,
			},
		},
	}
	redErr := r.Event(origErr)
	gotErr := redErr.Error.Headers["anthropic-ratelimit-requests-reset"]
	if strings.Contains(gotErr, secret) || !strings.Contains(gotErr, protocol.Redacted) {
		t.Errorf("error header not redacted: %q", gotErr)
	}
	if !strings.Contains(origErr.Error.Headers["anthropic-ratelimit-requests-reset"], secret) {
		t.Errorf("redacting error headers mutated caller map")
	}
}
