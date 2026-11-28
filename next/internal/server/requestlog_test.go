package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestCaptureKeepsHeadAndTail(t *testing.T) {
	c := (&RequestLog{All: true}).newCapture()
	head := bytes.Repeat([]byte("h\n"), loggedHead/2)
	middle := bytes.Repeat([]byte("m"), 3<<20)
	tail := append(bytes.Repeat([]byte("\nt"), (loggedTail-4)/2), "\nEND"...)
	for _, p := range [][]byte{head, middle, tail} {
		for len(p) > 0 {
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

func gapSecrets() []struct{ name, secret, spelled string } {
	unicode := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	nonASCII := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if r < 0x80 {
				b.WriteRune(r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
		return b.String()
	}
	percent := func(s string, keep string) string {
		var b strings.Builder
		for i := range len(s) {
			c := s[i]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "%%%02x", c)
			}
		}
		return b.String()
	}
	jsonInJSON := func(s string) string {
		once, _ := json.Marshal(s)
		twice, _ := json.Marshal(string(once))
		return string(twice[1 : len(twice)-1])
	}
	percent8 := func(s string) string {
		var b strings.Builder
		for i := range len(s) {
			fmt.Fprintf(&b, "%%%02X", s[i])
		}
		s = b.String()
		for range 7 {
			s = strings.ReplaceAll(s, "%", "%25")
		}
		return s
	}
	const key = "sk-test-0123456789abcdefghijklmnop"
	return []struct{ name, secret, spelled string }{
		{"plain", key, key},
		{"escaped slashes", "sk/test/0123456789abcdefghijklmnop", `sk\/test\/0123456789abcdefghijklmnop`},
		{"unicode escapes", key, unicode(key)},
		{"with spaces", "correct horse battery staple!", "correct horse battery staple!"},
		{"encodeURIComponent", "a!b c!d e!f g!h i!j", percent("a!b c!d e!f g!h i!j", "-_.!~*'()")},
		{"non-ASCII escapes", "Grüße!Passwort2024", nonASCII("Grüße!Passwort2024")},
		{"lowercase percent", "pässwörd: geheim", percent("pässwörd: geheim", "")},
		{"lowercase percent, non-ASCII only", "pässwörd: geheim", percent("pässwörd: geheim", ": ")},
		{"JSON in JSON", `tok_"quoted" & <tag>`, jsonInJSON(`tok_"quoted" & <tag>`)},
		{"percent-escaped 8 times", "k3y-abcd1234", percent8("k3y-abcd1234")},
	}
}

func gapFill(n int) string { return strings.Repeat("QZ;\n", n/4+1)[:n] }

func gapRedactor(secret string) *protocol.Redactor {
	return protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: secret})
}

func checkGaps(t *testing.T, log, s string, r *protocol.Redactor, gaps int) {
	t.Helper()
	if strings.Contains(log, s) || !strings.Contains(log, protocol.Redacted) {
		t.Fatalf("the whole copy of the secret is not redacted")
	}
	reach := gapWindow(r) + 2*len(s) + 64
	at := 0
	for range gaps {
		i := strings.Index(log[at:], " bytes not logged ...]")
		if i < 0 {
			t.Fatalf("fewer than %d cuts in the log", gaps)
		}
		at += i
		near := log[max(0, at-reach):min(len(log), at+reach)]
		if n, part := longestPart(near, s); n >= 4 {
			t.Errorf("%d bytes of the secret stay beside a cut: %q", n, part)
		}
		at++
	}
}

func gapOffsets(n int) []int {
	var out []int
	for k := 1; k < n; k++ {
		if k <= 24 || k >= n-24 || k%5 == 0 {
			out = append(out, k)
		}
	}
	return out
}

func writeAll(c *capture, body string) {
	for p := body; len(p) > 0; {
		n := min(len(p), 7001)
		c.Write([]byte(p[:n]))
		p = p[n:]
	}
}

func checkCount(t *testing.T, reply []byte, size int) {
	t.Helper()
	h, rest, ok := strings.Cut(string(reply), "\n[... ")
	if !ok {
		t.Fatalf("no cut in the reply")
	}
	var left int
	if n, err := fmt.Sscanf(rest, "%d bytes not logged ...]\n", &left); n != 1 {
		t.Fatal(err)
	}
	kept := len(h) + len(rest) - len(fmt.Sprintf("%d bytes not logged ...]\n", left))
	if kept+left != size {
		t.Errorf("%d bytes kept and %d said to be left out, of %d", kept, left, size)
	}
}

func TestCaptureDropsSecretPartsAtTheGap(t *testing.T) {
	logOf := func(l *RequestLog, c *capture) string {
		return string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
			reply: c, replyTitle: "RESPONSE"}))
	}
	for _, tc := range gapSecrets() {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spelled
			bodyOf := func(c *capture, k, middle int) string {
				return s + gapFill(c.headLimit-k-len(s)) + s + gapFill(middle) + s + gapFill(c.tailLimit-(len(s)-k))
			}
			modes := []bool{false}
			if tc.name == "plain" {
				modes = append(modes, true)
			}
			for _, all := range modes {
				l := &RequestLog{All: all, Redact: gapRedactor(tc.secret)}
				c := l.newCapture()
				body := bodyOf(c, len(s)/2, 4<<10)
				writeAll(c, body)
				checkCount(t, c.bytes(), len(body))
				checkGaps(t, logOf(l, c), s, l.Redact, 1)
			}
			l := &RequestLog{All: true, Redact: gapRedactor(tc.secret)}
			bound := gapWindow(l.Redact) + 4*len(s) + 256
			for _, k := range gapOffsets(len(s)) {
				c := &capture{headLimit: bound, tailLimit: bound, redact: l.Redact}
				body := bodyOf(c, k, 64)
				writeAll(c, body)
				checkCount(t, c.bytes(), len(body))
				checkGaps(t, logOf(l, c), s, l.Redact, 1)
				head, end := body[:c.headLimit], body[len(body)-c.tailLimit:]
				h, e := trimGap(l.Redact, head, end)
				lh, le := repeatTrim(l.Redact, head, end)
				if len(h) > len(lh) || len(e) > len(le) {
					t.Errorf("cut %d bytes in: one pass keeps %d and %d bytes, repeating keeps %d and %d", k, len(h), len(e), len(lh), len(le))
				}
			}
		})
	}
}

func repeatTrim(r *protocol.Redactor, head, end string) (string, string) {
	for {
		h, e := r.TrimCut(strings.TrimRightFunc(head, secretChar), strings.TrimLeftFunc(end, secretChar))
		if len(h) == len(head) && len(e) == len(end) {
			return h, e
		}
		head, end = h, e
	}
}

func TestUpstreamLogDropsSecretPartsAtTheGap(t *testing.T) {
	logOf := func(l *RequestLog, x protocol.UpstreamExchange) string {
		tr := newTrace()
		tr.captureUpstream(t.Context(), l.Redact)
		tr.upstream.add("acct", protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{x}})
		return string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"}, trace: tr,
			replyTitle: "RESPONSE"}))
	}
	for _, tc := range gapSecrets() {
		t.Run(tc.name, func(t *testing.T) {
			l := &RequestLog{All: true, Redact: gapRedactor(tc.secret)}
			s := tc.spelled
			for _, k := range gapOffsets(len(s)) {
				head := gapFill(gapWindow(l.Redact)+2*len(s)+128) + s + gapFill(gapWindow(l.Redact)+256) + s[:k]
				end := s[k:] + gapFill(gapWindow(l.Redact)+256)
				sent := protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + 1<<20}
				checkGaps(t, logOf(l, protocol.UpstreamExchange{
					Request: protocol.UpstreamRequest{Method: "POST", URL: "https://upstream.example.test/v1", CapturedBody: sent},
				}), s, l.Redact, 1)
			}
			k := len(s) / 2
			whole := s + gapFill(protocol.CapturedHead-k-len(s)) + s + gapFill(64<<10) + s + gapFill(protocol.CapturedEnd-(len(s)-k))
			checkGaps(t, logOf(l, protocol.UpstreamExchange{
				Request:  protocol.UpstreamRequest{Method: "POST", URL: "https://upstream.example.test/v1"},
				Response: &protocol.UpstreamResponse{Status: 200, CapturedBody: protocol.CapturedBody{Body: whole, BodySize: int64(len(whole))}},
			}), s, l.Redact, 1)
		})
	}
}

func TestCaptureKeepsCopiesAcrossTheWindowWhole(t *testing.T) {
	for _, tc := range gapSecrets() {
		l := &RequestLog{All: true, Redact: gapRedactor(tc.secret)}
		n, s := gapWindow(l.Redact), tc.spelled
		if n < len(s) {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, j := range gapOffsets(len(s)) {
				head := s + gapFill(256) + s + gapFill(n-(len(s)-j))
				end := gapFill(n-j) + s + gapFill(256) + s
				c := &capture{headLimit: len(head), tailLimit: len(end), redact: l.Redact}
				body := head + gapFill(64) + end
				writeAll(c, body)
				reply := c.bytes()
				checkCount(t, reply, len(body))
				log := string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
					reply: c, replyTitle: "RESPONSE"}))
				if n, part := longestPart(log, s); n >= 4 {
					t.Fatalf("across the window's edge %d bytes in: %d bytes of the secret stay: %q", j, n, part)
				}
				if got := strings.Count(log, protocol.Redacted); got < 2 {
					t.Fatalf("across the window's edge %d bytes in: %d copies redacted, want the 2 outside the windows", j, got)
				}
			}
		})
	}
}

func TestCaptureKeyOnlySecretsHaveNoWindow(t *testing.T) {
	jwt := strings.Repeat("eyJhbGciOiJSUzI1NiJ9", 100)[:2000]
	jwt = jwt[:400] + "." + jwt[401:1600] + "." + jwt[1601:]
	l := &RequestLog{Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-client-0123456789"},
		&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: jwt, RefreshToken: "rt-0123456789abcdef"})}
	if n := gapWindow(l.Redact); n != 0 {
		t.Fatalf("window %d bytes, want 0", n)
	}
	c := l.newCapture()
	k := 1000
	body := gapFill(c.headLimit-k) + jwt + gapFill(c.tailLimit-(len(jwt)-k)+4096)
	writeAll(c, body)
	reply := string(c.bytes())
	checkCount(t, []byte(reply), len(body))
	head, _, _ := strings.Cut(reply, "\n[... ")
	if want := c.headLimit - k - 2; len(head) < want {
		t.Errorf("kept %d bytes of the start, want at least %d of %d", len(head), want, c.headLimit)
	}
	if n, part := longestPart(head[max(0, len(head)-4096):], jwt); n >= 4 {
		t.Errorf("%d bytes of the token stay beside the cut: %q", n, part)
	}
}

func testToken() string { return strings.Repeat("eyJhbGciOiJSUzI1NiJ9AbC", 100)[:2000] }

func cutHead(l *RequestLog, head string) (string, []byte) {
	c := &capture{headLimit: len(head), tailLimit: 64, redact: l.Redact}
	writeAll(c, head+gapFill(64)+gapFill(64))
	return string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
		reply: c, replyTitle: "RESPONSE"})), c.bytes()
}

func TestKeepWholeDropsTheSideWhereTheRedactionGivesUp(t *testing.T) {
	tok := testToken()
	l := &RequestLog{All: true, Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "pass word!"},
		&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tok})}
	n := gapWindow(l.Redact)
	giveUp := "%30%5C" + "\\" + "u0035%5C" + "\\" + "\\" + "u0070%2535"
	if l.Redact.String("x;"+giveUp+"y") != protocol.Redacted {
		t.Fatal("the redaction does not give up on the text")
	}
	for extra := 0; extra < 2*n; extra++ {
		head := gapFill(500) + tok + ";" + giveUp + strings.Repeat(" ", extra)
		log, reply := cutHead(l, head)
		checkCount(t, reply, len(head)+128)
		if k, part := longestPart(log, tok); k >= 4 {
			t.Fatalf("with %d spaces after the text: %d bytes of the token stay: %.40q", extra, k, part)
		}
	}
}

func TestKeepWholeDoesNotStopInsideAToken(t *testing.T) {
	tok := testToken()
	l := &RequestLog{All: true, Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k9z"},
		&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "hi k9z yo there"},
		&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tok})}
	n := gapWindow(l.Redact)
	spelled := "hi " + "\\" + "u006b9z yo there"
	for edge := 1; edge < len(spelled); edge++ {
		head := gapFill(500) + tok + spelled + strings.Repeat(";", n-(len(spelled)-edge))
		log, reply := cutHead(l, head)
		checkCount(t, reply, len(head)+128)
		if k, part := longestPart(log, tok); k >= 4 {
			t.Fatalf("with the edge %d bytes into the copy: %d bytes of the token stay: %.40q", edge, k, part)
		}
	}
}

func TestCaptureKeepsRepeatedSecretStarts(t *testing.T) {
	var cs []*protocol.Credential
	for _, f := range upheader.Forms(map[string]string{"Authorization": "Bearer sk-test-header-0123456789"}) {
		cs = append(cs, &protocol.Credential{Kind: "header", APIKey: f})
	}
	l := &RequestLog{Redact: protocol.NewRedactor(cs...)}
	c := l.newCapture()
	unit := `"Authorization":`
	writeAll(c, strings.Repeat(unit, (c.headLimit+c.tailLimit+(1<<20))/len(unit)))
	reply := string(c.bytes())
	head, end, _ := strings.Cut(reply, "\n[... ")
	_, end, _ = strings.Cut(end, " ...]\n")
	most := gapWindow(l.Redact) + 2*len(unit)
	if len(head) < c.headLimit-most || len(end) < c.tailLimit-most {
		t.Errorf("kept %d bytes of the start and %d of the end, want at least %d and %d", len(head), len(end), c.headLimit-most, c.tailLimit-most)
	}
}

func longestPart(text, s string) (int, string) {
	best := ""
	for i := range s {
		for j := i + len(best) + 1; j <= len(s) && strings.Contains(text, s[i:j]); j++ {
			best = s[i:j]
		}
	}
	return len(best), best
}

func TestRequestLogKeptWhenHandlerPanics(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"panic-body"}]}`)
	for _, tc := range []struct {
		name   string
		handle func(w http.ResponseWriter)
		want   []string
		absent []string
	}{
		{"after the reply", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"panic-reply"}`)
		}, []string{"Status: 503", "panic-reply"}, []string{"Status: 200"}},
		{"before the reply", func(http.ResponseWriter) {}, nil, []string{"Status:"}},
		{"after a 200 reply", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"ok":"panic-reply"}`)
		}, []string{"Status: 200", "panic-reply"}, []string{"Status: 503"}},
	} {
		for _, all := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, request log %t", tc.name, all), func(t *testing.T) {
				dir := t.TempDir()
				g := &Generation{RequestLog: &RequestLog{Dir: dir, All: all, Redact: protocol.NewRedactor()}}
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				req.Header.Set("X-Test-Header", "kept")
				panicValue := fmt.Errorf("handler panic, %s", tc.name)
				var recovered any
				func() {
					defer func() { recovered = recover() }()
					logged(httptest.NewRecorder(), req, g, func(w http.ResponseWriter, r *http.Request) {
						keepBody(r, body)
						tc.handle(w)
						panic(panicValue)
					})
				}()
				if recovered != panicValue {
					t.Fatalf("the panic reached the caller as %v, want the same value %v", recovered, panicValue)
				}

				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatalf("request log files after the panic: %d, want 1", len(entries))
				}
				if got := strings.HasPrefix(entries[0].Name(), "error-"); got != !all {
					t.Errorf("the panic's log is %s, want an error log: %t", entries[0].Name(), !all)
				}
				raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
				if err != nil {
					t.Fatal(err)
				}
				text := string(raw)
				for _, want := range append([]string{"URL: /v1/chat/completions", "Method: POST", "X-Test-Header: kept",
					"panic-body", "=== RESPONSE ==="}, tc.want...) {
					if !strings.Contains(text, want) {
						t.Errorf("the panic's request log lacks %q:\n%s", want, text)
					}
				}
				for _, absent := range tc.absent {
					if strings.Contains(text, absent) {
						t.Errorf("the panic's request log holds %q, which the request never sent:\n%s", absent, text)
					}
				}
			})
		}
	}
}

func TestRequestLogErrorsOnlySkipsOKReply(t *testing.T) {
	dir := t.TempDir()
	g := &Generation{RequestLog: &RequestLog{Dir: dir, Redact: protocol.NewRedactor()}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	logged(httptest.NewRecorder(), req, g, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":true}`)
	})
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("request log files for a 200 reply with error logs only: %d, want 0", len(entries))
	}
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

func TestRequestLogAppearsWhole(t *testing.T) {
	dir := t.TempDir()
	l := &RequestLog{Dir: dir, All: true}
	e := &requestEntry{at: time.Now(), method: "POST", url: &url.URL{Path: "/v1/responses"}, trace: newTrace(),
		body: bytes.Repeat([]byte("b"), 32<<20), replyTitle: "RESPONSE"}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var seen []int64
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
