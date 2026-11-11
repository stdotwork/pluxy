package server

import (
	"bytes"
	"strings"
	"testing"
)

// The secrets below are not any account's credential or proxy password, so nothing but
// upstreamURL can remove them.
func TestUpstreamURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      string // exact; empty when only notWant is checked
		notWant   []string
	}{
		{name: "plain", raw: "https://api.example.test/v1/chat?model=m", want: "https://api.example.test/v1/chat?model=m"},
		{name: "user info", raw: "https://user:pw-fake@api.example.test/v1", want: "https://api.example.test/v1"},
		{name: "secret value", raw: "https://api.example.test/v1?model=m&api_key=other-secret", want: "https://api.example.test/v1?api_key=%5Bredacted%5D&model=m"},
		{name: "semicolon", raw: "https://api.example.test/v1?api_key=other-secret;mode=test", want: "https://api.example.test/v1?[redacted]"},
		{name: "bad escape", raw: "https://api.example.test/v1?api_key=other-secret%ZZ", want: "https://api.example.test/v1?[redacted]"},
		{name: "bad escape in the key", raw: "https://api.example.test/v1?api_key%ZZ=other-secret", want: "https://api.example.test/v1?[redacted]"},
		{name: "does not parse", raw: "https://user:pw-fake@api.example.test/v1%zz?api_key=other-secret", want: unloggedURL},
		{name: "does not parse, bad host", raw: "https://user:pw-fake@api example.test/v1?api_key=other-secret", want: unloggedURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamURL(tc.raw)
			if got != tc.want {
				t.Errorf("upstreamURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for _, s := range []string{"other-secret", "pw-fake"} {
				if strings.Contains(got, s) {
					t.Errorf("upstreamURL(%q) = %q, which holds %q", tc.raw, got, s)
				}
			}
		})
	}
}

func TestWriteUpstreamHeadersCountsLeftOut(t *testing.T) {
	var b bytes.Buffer
	writeUpstreamHeaders(&b, map[string][]string{"X-A": {"1"}}, 3)
	if got, want := b.String(), "X-A: 1\n[... 3 header lines not logged ...]\n"; got != want {
		t.Errorf("headers = %q, want %q", got, want)
	}
	b.Reset()
	writeUpstreamHeaders(&b, map[string][]string{"X-A": {"1"}}, 0)
	if got, want := b.String(), "X-A: 1\n"; got != want {
		t.Errorf("headers = %q, want %q", got, want)
	}
}
