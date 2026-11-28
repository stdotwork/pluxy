package host

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

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
		{"duration of a numeric key's digits", "4829175306418", "x-ratelimit-reset-tokens", "4829175306s", ""},
		{"time with a key's digits as its fraction", "4829175306418", "anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00.4829175306Z", ""},
		{"duration with a long number of a key", "sk-4829175306418abcdef", "x-ratelimit-reset-tokens", "1h2m4829175306s", ""},
		{"time with a fraction that shares nothing", "pluxy1500key", "anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00.5Z", "2026-11-22T08:00:00.5Z"},
		{"minus one", "pluxy1500key", "x-ratelimit-limit-tokens", "-1", "-1"},
		{"text with a piece of the key", "sk-sweepSecretKey0123456789abcdefXYZ", "x-ratelimit-limit-tokens", "sk-sweepSecret", protocol.Redacted},
	} {
		p := pieces{tc.key}
		h := map[string]string{tc.header: tc.value}
		errEv := p.event(llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Headers: h}})
		check := func(where, got string) {
			t.Helper()
			if tc.want == "" {
				if llm.ValidRateLimitHeaderValue(got) {
					t.Errorf("%s: %s header = %q, want it dropped as invalid", tc.name, where, got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("%s: %s header = %q, want %q", tc.name, where, got, tc.want)
			}
		}
		check("error", errEv.Error.Headers[tc.header])
		respEv := p.event(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Headers: h}})
		check("response", respEv.Response.Headers[tc.header])
		if h[tc.header] != tc.value {
			t.Errorf("%s: the event's own headers changed to %q", tc.name, h[tc.header])
		}
	}
}
