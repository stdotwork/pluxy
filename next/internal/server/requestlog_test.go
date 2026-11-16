package server

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A long reply keeps its start and its end, with the bytes left out counted in between.
func TestCaptureKeepsHeadAndTail(t *testing.T) {
	c := (&RequestLog{All: true}).newCapture()
	// Lines, so that nothing beside the cut could be part of a secret (trimGap).
	head := bytes.Repeat([]byte("h\n"), loggedHead/2)
	middle := bytes.Repeat([]byte("m"), 3<<20)
	tail := append(bytes.Repeat([]byte("\nt"), (loggedTail-4)/2), "\nEND"...)
	for _, p := range [][]byte{head, middle, tail} {
		for len(p) > 0 { // in uneven writes, as a stream arrives
			n := min(len(p), 70001)
			c.Write(p[:n])
			p = p[n:]
		}
	}
	got := c.bytes()
	want := string(head) + "\n[... 3145728 bytes not logged ...]\n" + string(tail)
	if string(got) != want {
		t.Fatalf("got %d bytes, starting %q and ending %q", len(got), got[:10], got[len(got)-10:])
	}
	short := (&RequestLog{}).newCapture()
	short.Write([]byte("short reply"))
	if string(short.bytes()) != "short reply" {
		t.Fatalf("short: %q", short.bytes())
	}
}

// gapSecrets are secrets as the request log redacts them (the config's), each with a spelling a
// reply could carry it in.
func gapSecrets() []struct{ name, secret, spelled string } {
	unicode := func(s string) string { // every character as a JSON \u escape
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	const key = "sk-test-0123456789abcdefghijklmnop" // made up
	return []struct{ name, secret, spelled string }{
		{"plain", key, key},
		{"escaped slashes", "sk/test/0123456789abcdefghijklmnop", `sk\/test\/0123456789abcdefghijklmnop`},
		{"unicode escapes", key, unicode(key)},
		{"with spaces", "correct horse battery staple!", "correct horse battery staple!"},
	}
}

// gapFill returns n bytes of text made of bytes no spelling in gapSecrets holds, so any of a
// secret's bytes in a log beside a cut is a part of the secret.
func gapFill(n int) string { return strings.Repeat("QZ; ", n/4+1)[:n] }

// checkGaps fails t where log does not redact the whole secret s, has fewer than gaps cuts, or
// keeps 4 bytes or more of s beside one of them.
func checkGaps(t *testing.T, log, s string, gaps int) {
	t.Helper()
	if strings.Contains(log, s) || !strings.Contains(log, protocol.Redacted) {
		t.Fatalf("the whole copy of the secret is not redacted")
	}
	at := 0
	for range gaps {
		i := strings.Index(log[at:], " bytes not logged ...]")
		if i < 0 {
			t.Fatalf("fewer than %d cuts in the log", gaps)
		}
		at += i
		near := log[max(0, at-2*len(s)-64):min(len(log), at+2*len(s)+64)]
		if n, part := longestPart(near, s); n >= 4 {
			t.Errorf("%d bytes of the secret stay beside a cut: %q", n, part)
		}
		at++
	}
}

// A secret that the middle a long reply's log leaves out cuts in two is not kept in part beside
// the cut, however it is spelled: neither part is the whole secret, so the redaction cannot
// recognise it.
func TestCaptureDropsSecretPartsAtTheGap(t *testing.T) {
	for _, tc := range gapSecrets() {
		for _, all := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/all=%v", tc.name, all), func(t *testing.T) {
				l := &RequestLog{All: all, Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tc.secret})}
				c := l.newCapture()
				s, k := tc.spelled, len(tc.spelled)/2
				// A whole copy at the start, which the redaction removes; a copy across the end of
				// the kept start; and one across the start of the kept end.
				body := s + gapFill(c.headLimit-k-len(s)) + s + gapFill(1<<20) + s + gapFill(c.tailLimit-(len(s)-k))
				for p := body; len(p) > 0; { // in uneven writes, as a stream arrives
					n := min(len(p), 7001)
					c.Write([]byte(p[:n]))
					p = p[n:]
				}
				reply := c.bytes()
				// What was left out beside the cut counts with the bytes not logged.
				var kept, left int
				if h, rest, ok := strings.Cut(string(reply), "\n[... "); !ok {
					t.Fatalf("no cut in the reply")
				} else if n, err := fmt.Sscanf(rest, "%d bytes not logged ...]\n", &left); n != 1 {
					t.Fatal(err)
				} else {
					kept = len(h) + len(rest) - len(fmt.Sprintf("%d bytes not logged ...]\n", left))
				}
				if kept+left != len(body) {
					t.Errorf("%d bytes kept and %d said to be left out, of %d", kept, left, len(body))
				}
				checkGaps(t, string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
					reply: reply, replyTitle: "RESPONSE"})), s, 1)
			})
		}
	}
}

// The same holds for an upstream exchange's bodies: one the extension sent cut, and one core
// cuts to the bounds.
func TestUpstreamLogDropsSecretPartsAtTheGap(t *testing.T) {
	for _, tc := range gapSecrets() {
		t.Run(tc.name, func(t *testing.T) {
			l := &RequestLog{All: true, Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tc.secret})}
			tr := newTrace()
			tr.captureUpstream(t.Context(), l.Redact)
			s, k := tc.spelled, len(tc.spelled)/2
			head := s + gapFill(protocol.CapturedHead-k-len(s)) + s[:k]
			end := s[k:] + gapFill(protocol.CapturedEnd-(len(s)-k))
			sent := protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + 1<<20}
			whole := head + s[k:] + gapFill(64<<10) + s[:k] + end
			tr.upstream.add("acct", protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{
				Request:  protocol.UpstreamRequest{Method: "POST", URL: "https://upstream.example.test/v1", CapturedBody: sent},
				Response: &protocol.UpstreamResponse{Status: 200, CapturedBody: protocol.CapturedBody{Body: whole, BodySize: int64(len(whole))}},
			}}})
			checkGaps(t, string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"}, trace: tr,
				replyTitle: "RESPONSE"})), s, 2)
		})
	}
}

// longestPart returns the longest run of s's bytes that text holds, and its length.
func longestPart(text, s string) (int, string) {
	best := ""
	for i := range s {
		for j := len(s); j > i+len(best); j-- {
			if strings.Contains(text, s[i:j]) {
				best = s[i:j]
				break
			}
		}
	}
	return len(best), best
}

func TestLogFileName(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 5, 0, time.UTC)
	if got := logFileName("/v1beta/models/gemini-2.5:generateContent", at, "20261109120005-ab"); got != "v1beta-models-gemini-2.5-generateContent-2026-11-09T120005-20261109120005-ab.log" {
		t.Fatalf("got %q", got)
	}
	long := logFileName("/"+strings.Repeat("a", 300), at, "20261109120005-0123456789abcdef")
	if len(long) > 160 || !IsErrorLogName("error-"+long) {
		t.Fatalf("long path: %d bytes, %q", len(long), long)
	}
	const ok = "error-v1-chat-completions-2026-11-09T120005-20261109120005-0123456789abcdef.log"
	for name, want := range map[string]bool{ok: true, ok[len("error-"):]: false, "error-v1-x.log": false, "error-x.txt": false,
		`error-a\b-2026-11-09T120005-20261109120005-0123456789abcdef.log`:                              false,
		"error-" + strings.Repeat("a", 101) + "-2026-11-09T120005-20261109120005-0123456789abcdef.log": false,
		"error-" + strings.Repeat("a", 100) + "-2026-11-09T120005-20261109120005-0123456789abcdef.log": true, "error-../a-2026-11-09T120005-20261109120005-0123456789abcdef.log": false} {
		if IsErrorLogName(name) != want {
			t.Errorf("IsErrorLogName(%q) != %v", name, want)
		}
	}
}

// Only a request log's name, ending in a trace ID, is found by one; other log files are not.
func TestFindRequestLog(t *testing.T) {
	dir := t.TempDir()
	const id = "20261109120000-0123456789abcdef"
	for _, n := range []string{"notes-abc.log", "main-abc.log", "notes-" + id + ".log", "v1-chat-completions-2026-11-09T120000-" + id + ".log"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := FindRequestLog(dir, "abc"); got != "" {
		t.Fatalf("abc found %q", got)
	}
	if got := FindRequestLog(dir, id); got != "v1-chat-completions-2026-11-09T120000-"+id+".log" {
		t.Fatalf("trace ID found %q", got)
	}
}

// A reader of the logs folder finds a request log whole: never the file with part of its content.
func TestRequestLogAppearsWhole(t *testing.T) {
	dir := t.TempDir()
	l := &RequestLog{Dir: dir, All: true}
	e := &requestEntry{at: time.Now(), method: "POST", url: &url.URL{Path: "/v1/responses"}, trace: newTrace(),
		body: bytes.Repeat([]byte("b"), 32<<20), replyTitle: "RESPONSE"} // big, so the write takes a while

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var seen []int64 // the size of the log file at each look that found it
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if matches, _ := filepath.Glob(filepath.Join(dir, "*.log")); len(matches) == 1 {
				if info, err := os.Stat(matches[0]); err == nil {
					seen = append(seen, info.Size())
				}
			}
		}
	}()
	l.write(e)
	close(stop)
	wg.Wait()

	matches, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(matches) != 1 {
		t.Fatalf("request log files: %v", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range seen {
		if size != info.Size() {
			t.Fatalf("a reader found the log at %d bytes, of %d once written", size, info.Size())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries after the write, want only the log", len(entries))
	}
}
