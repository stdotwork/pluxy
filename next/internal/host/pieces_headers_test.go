package host

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestPiecesRateLimitHeaders checks what core's piece removal does to rate-limit header values
// on an error reply and on a response.start: a time or a duration stays whole whatever the key
// shares with it, and an integer, or text that is not a rate-limit shape, loses a piece of the
// key. A value a piece took anything from is not a valid rate-limit value any more, so the
// server drops it.
func TestPiecesRateLimitHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, key, header, value string
		want                     string
	}{
		{"time with a year in the key", "team-2026-key", "anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00Z", "2026-11-22T08:00:00Z"},
		{"time with a zero run in the key", "k00:00:00Zk", "anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00Z", "2026-11-22T08:00:00Z"},
		{"duration with its characters in the key", "pw6m0spw", "x-ratelimit-reset-tokens", "6m0s", "6m0s"},
		{"count that shares digits with the key", "pluxy1500key", "x-ratelimit-limit-tokens", "150000", protocol.Redacted},
		{"count that is a piece of a numeric key", "4829175306418", "x-ratelimit-limit-requests", "4829175306", protocol.Redacted},
		{"count that shares nothing with the key", "pluxy1500key", "x-ratelimit-limit-requests", "60", "60"},
		{"text with a piece of the key", "sk-sweepSecretKey0123456789abcdefXYZ", "x-ratelimit-limit-tokens", "sk-sweepSecret", protocol.Redacted},
	} {
		p := pieces{tc.key}
		h := map[string]string{tc.header: tc.value}
		errEv := p.event(llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Headers: h}})
		if got := errEv.Error.Headers[tc.header]; got != tc.want {
			t.Errorf("%s: error header = %q, want %q", tc.name, got, tc.want)
		}
		respEv := p.event(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Headers: h}})
		if got := respEv.Response.Headers[tc.header]; got != tc.want {
			t.Errorf("%s: response header = %q, want %q", tc.name, got, tc.want)
		}
		if h[tc.header] != tc.value {
			t.Errorf("%s: the event's own headers changed to %q", tc.name, h[tc.header])
		}
	}
}
