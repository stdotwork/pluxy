package server

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func TestUpstreamURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      string
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

func TestWriteBodyStopped(t *testing.T) {
	const stopLine = "\n[... the body was not read past here ...]"
	const odd = "(q) [z] {x} !v? @j; #k: *9^ ~7|"
	const keyOnly = "sk-Stop/Key+0123456789abcdef"
	oddLog := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: odd})
	keyLog := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: keyOnly})
	write := func(r *protocol.Redactor, c protocol.CapturedBody) string {
		var b bytes.Buffer
		writeBody(&b, c, r)
		return b.String()
	}
	stopped := func(body string) protocol.CapturedBody {
		return protocol.CapturedBody{Body: body, BodySize: int64(len(body)), BodyStopped: true}
	}
	noPart := func(t *testing.T, got string, spellings ...string) {
		t.Helper()
		for _, s := range spellings {
			for i := 0; i+4 <= len(s); i++ {
				if strings.Contains(got, s[i:i+4]) {
					t.Fatalf("the log keeps %q of the secret where the read stopped: %q", s[i:i+4], got)
				}
			}
		}
	}
	t.Run("config secret's start", func(t *testing.T) {
		got := write(oddLog, stopped("event one\nMARK "+odd[:10]))
		noPart(t, got, odd)
		if !strings.HasPrefix(got, "event one\n") || !strings.HasSuffix(got, stopLine) {
			t.Errorf("got %q, want the first line kept and the line saying the read stopped", got)
		}
	})
	t.Run("config secret's start in escapes outside its forms", func(t *testing.T) {
		const umlaut = "pässwort!Geheim-2024-xyz"
		const spelled = `p\u00e4sswort!Geheim-2024-xyz`
		r := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: umlaut})
		got := write(r, stopped("event one\nMARK "+spelled[:16]))
		noPart(t, got, umlaut, spelled)
		if !strings.HasPrefix(got, "event one\n") || !strings.HasSuffix(got, stopLine) {
			t.Errorf("got %q, want the first line kept and the line saying the read stopped", got)
		}
	})
	t.Run("key-character secret's start spelled outside its forms", func(t *testing.T) {
		spelled := "sk-Stop%2fKey%2b0123456789abcdef"
		for _, body := range []string{`{"echo": "` + spelled[:20], `{"echo": "` + strings.ReplaceAll(spelled, "%2f", `\u002f`)[:22]} {
			got := write(keyLog, stopped(body))
			noPart(t, got, keyOnly, spelled)
			if want := `{"echo": "` + stopLine; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		}
	})
	t.Run("stream stopped after its last event", func(t *testing.T) {
		sse := "data: {\"a\":1}\n\ndata: [DONE]\n\n"
		if got := write(oddLog, stopped(sse)); got != sse+stopLine {
			t.Errorf("got %q, want the stream kept whole, then the line saying the read stopped", got)
		}
	})
	t.Run("stream stopped inside an event", func(t *testing.T) {
		sse := "data: {\"a\":1}\n\n"
		got := write(oddLog, stopped(sse+`data: {"b":"MARK `+odd[:10]))
		if got != sse+stopLine {
			t.Errorf("got %q, want the whole events kept and the cut one left out, then the line", got)
		}
	})
	t.Run("cut body", func(t *testing.T) {
		head, end := strings.Repeat("h ", 2000), strings.Repeat("e ", 2000)+"MARK "+odd[:10]
		got := write(oddLog, protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + 5000, BodyStopped: true})
		noPart(t, got, odd)
		if !strings.Contains(got, " bytes not logged ...]\n") || !strings.Contains(got, "e e e e") || !strings.HasSuffix(got, stopLine) {
			t.Errorf("got %q, want the start, the gap's line, the end without the secret's start, then the stop's line", got)
		}
	})
	t.Run("unmarked", func(t *testing.T) {
		body := "event one\nMARK " + odd[:10]
		if got := write(oddLog, protocol.CapturedBody{Body: body, BodySize: int64(len(body))}); got != body {
			t.Errorf("a body read to its end was written as %q, want %q", got, body)
		}
	})
}
