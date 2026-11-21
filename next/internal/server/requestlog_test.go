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
// reply could carry it in that the redaction removes whole: forms it lists (as is, JSON-escaped)
// and spellings it only decodes (escapes of every character, of non-ASCII characters only,
// lowercase percent-escapes, JSON quoted in JSON, percent-escapes 8 layers deep).
func gapSecrets() []struct{ name, secret, spelled string } {
	unicode := func(s string) string { // every character as a JSON \u escape
		var b strings.Builder
		for _, r := range s {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		return b.String()
	}
	nonASCII := func(s string) string { // as Python's json.dumps writes it: \u for non-ASCII only
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
	percent := func(s string, keep string) string { // lowercase percent-escapes but for letters, digits and keep
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
	jsonInJSON := func(s string) string { // a JSON string holding s, inside a JSON string
		once, _ := json.Marshal(s)
		twice, _ := json.Marshal(string(once))
		return string(twice[1 : len(twice)-1])
	}
	percent8 := func(s string) string { // every byte percent-escaped, then its percent signs 7 times more
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
	const key = "sk-test-0123456789abcdefghijklmnop" // made up
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
		// Longer than the window trimGap leaves out (12 bytes for each of the 16 of the secret's
		// base64), so only the run of key characters beyond the window removes it.
		{"percent-escaped 8 times", "k3y-abcd1234", percent8("k3y-abcd1234")},
	}
}

// gapFill returns n bytes of text made of bytes no spelling in gapSecrets holds, so any of a
// secret's bytes in a log beside a cut is a part of the secret.
func gapFill(n int) string { return strings.Repeat("QZ;\n", n/4+1)[:n] }

// gapRedactor returns the redactor of a config whose secret is secret.
func gapRedactor(secret string) *protocol.Redactor {
	return protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: secret})
}

// checkGaps fails t where log does not redact the whole secret s, has fewer than gaps cuts, or
// keeps 4 bytes or more of s anywhere a part of it could reach from one of them: within the
// bytes trimGap leaves out for r and a spelling's length.
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

// gapOffsets returns the places in a spelling n bytes long that the tests cut it at: every place
// within 24 bytes of either end, where what is left on one side is short, and every 5th place
// between them. Along a spelling made of escapes of one length, not a multiple of 5 (3 for %XX, 6
// for \uXXXX, 17 for a byte of the 8-layer spelling), that lands at each place inside an escape
// once the middle is 5 escapes long; a shorter spelling is cut at every place.
func gapOffsets(n int) []int {
	var out []int
	for k := 1; k < n; k++ {
		if k <= 24 || k >= n-24 || k%5 == 0 {
			out = append(out, k)
		}
	}
	return out
}

// writeAll writes body to c in uneven pieces, as a stream arrives.
func writeAll(c *capture, body string) {
	for p := body; len(p) > 0; {
		n := min(len(p), 7001)
		c.Write([]byte(p[:n]))
		p = p[n:]
	}
}

// checkCount fails t unless the bytes reply keeps and the bytes it says it left out add up to
// size: what trimGap leaves out is counted.
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

// A secret that the middle a long reply's log leaves out cuts in two is not kept in part beside
// the cut, wherever in the secret the cut falls and however the secret is spelled: neither part
// is the whole secret, so the redaction cannot recognise it.
func TestCaptureDropsSecretPartsAtTheGap(t *testing.T) {
	logOf := func(l *RequestLog, c *capture) string {
		return string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
			reply: c, replyTitle: "RESPONSE"}))
	}
	for _, tc := range gapSecrets() {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spelled
			// A whole copy at the start, which the redaction removes; a copy across the end of the
			// kept start; and one across the start of the kept end, each cut k bytes in.
			bodyOf := func(c *capture, k, middle int) string {
				return s + gapFill(c.headLimit-k-len(s)) + s + gapFill(middle) + s + gapFill(c.tailLimit-(len(s)-k))
			}
			// The request log's own bounds: with request-log off for each spelling, and on (the
			// same code, with bounds 8 times larger) for the plain one.
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
			// Every place in the secret the cut can fall, with bounds that just hold the copies and
			// the window. trimGap leaves out at least what repeating its TrimCut and its run of key
			// characters until they find no more does.
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

// repeatTrim leaves out beside a cut the run of key characters and a part of a form of one of r's
// secrets, over and over until neither finds more.
func repeatTrim(r *protocol.Redactor, head, end string) (string, string) {
	for {
		h, e := r.TrimCut(strings.TrimRightFunc(head, secretChar), strings.TrimLeftFunc(end, secretChar))
		if len(h) == len(head) && len(e) == len(end) {
			return h, e
		}
		head, end = h, e
	}
}

// The same holds for an upstream exchange's bodies: one the extension sent cut, wherever in the
// secret the cut falls, and one core cuts to the bounds.
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
				// Text first, so that what checkGaps looks at near the cut stays in the body, away from
				// the log's random trace ID.
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

// A whole copy of a secret that the edge of the window left out beside the cut falls across is
// not cut there: the edge moves to where the copy starts (or, on the end's side, ends), so that
// the redaction still finds the copy whole, however it is spelled. Only secrets with a character
// other than a key's have a window.
func TestCaptureKeepsCopiesAcrossTheWindowWhole(t *testing.T) {
	for _, tc := range gapSecrets() {
		l := &RequestLog{All: true, Redact: gapRedactor(tc.secret)}
		n, s := gapWindow(l.Redact), tc.spelled
		if n < len(s) { // no window, or one too short for a copy to fall across its edge alone
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			// Each side holds a whole copy before the window and one across the window's edge, j
			// bytes into it.
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

// A config whose secrets are made only of key characters, such as a client key and an OAuth
// account's 2,000-byte token, has no window: a token cut by the gap goes with the run of key
// characters, and the rest of the kept start stays.
func TestCaptureKeyOnlySecretsHaveNoWindow(t *testing.T) {
	jwt := strings.Repeat("eyJhbGciOiJSUzI1NiJ9", 100)[:2000] // made up, of a token's characters
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
	// The token's 1,000 bytes, and the key characters of the text before it ("QZ").
	if want := c.headLimit - k - 2; len(head) < want {
		t.Errorf("kept %d bytes of the start, want at least %d of %d", len(head), want, c.headLimit)
	}
	if n, part := longestPart(head[max(0, len(head)-4096):], jwt); n >= 4 {
		t.Errorf("%d bytes of the token stay beside the cut: %q", n, part)
	}
}

// testToken is a made-up OAuth token, 2,000 key characters: longer than any window.
func testToken() string { return strings.Repeat("eyJhbGciOiJSUzI1NiJ9AbC", 100)[:2000] }

// cutHead returns the request log of a reply whose kept start is head, and the reply's bytes.
func cutHead(l *RequestLog, head string) (string, []byte) {
	c := &capture{headLimit: len(head), tailLimit: 64, redact: l.Redact}
	writeAll(c, head+gapFill(64)+gapFill(64))
	return string(l.format(&requestEntry{method: "POST", url: &url.URL{Path: "/v1/chat/completions"},
		reply: c, replyTitle: "RESPONSE"})), c.bytes()
}

// Where the redaction gives up on the text near the window's edge (escapes that decode too many
// ways), the edge does not stop inside the token before that text, which no check around the edge
// sees whole: a move measured on what the redaction gave back would put it there. That side goes
// (keepWhole gives up too), and the run of key characters after each move would also take the
// token.
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

// A move that the redaction makes longer than the text it stands for (here a 3-byte secret
// spelled in 8 bytes, inside a copy of a longer secret, is replaced by the 10 of Redacted) does not
// stop inside the token before the copy: a move whose length is not known in the text goes back
// a whole window, and the run of key characters there goes after it.
func TestKeepWholeDoesNotStopInsideAToken(t *testing.T) {
	tok := testToken()
	l := &RequestLog{All: true, Redact: protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k9z"},
		&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "hi k9z yo there"},
		&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tok})}
	n := gapWindow(l.Redact)
	spelled := "hi " + "\\" + "u006b9z yo there" // "hi k9z yo there", its k escaped
	for edge := 1; edge < len(spelled); edge++ {
		// The window's edge falls edge bytes into the copy, which follows the token at once.
		head := gapFill(500) + tok + spelled + strings.Repeat(";", n-(len(spelled)-edge))
		log, reply := cutHead(l, head)
		checkCount(t, reply, len(head)+128)
		if k, part := longestPart(log, tok); k >= 4 {
			t.Fatalf("with the edge %d bytes into the copy: %d bytes of the token stay: %.40q", edge, k, part)
		}
	}
}

// Text that repeats the start of a form of a secret beside the cut, as a reply that repeats a
// secret header's name can, loses no more than other text there: most of what the log keeps of
// the reply's start and end stays.
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
	// The window, the rest of a repeat's run of key characters, and one start of a form.
	most := gapWindow(l.Redact) + 2*len(unit)
	if len(head) < c.headLimit-most || len(end) < c.tailLimit-most {
		t.Errorf("kept %d bytes of the start and %d of the end, want at least %d and %d", len(head), len(end), c.headLimit-most, c.tailLimit-most)
	}
}

// longestPart returns the longest run of s's bytes that text holds, and its length.
func longestPart(text, s string) (int, string) {
	best := ""
	for i := range s {
		// Only a run longer than the best so far matters; grow it while text holds it.
		for j := i + len(best) + 1; j <= len(s) && strings.Contains(text, s[i:j]); j++ {
			best = s[i:j]
		}
	}
	return len(best), best
}

// A handler's panic does not lose the request's log: the request that crashed is the one an
// operator most needs, and the panic still reaches the server unchanged. A reply the handler
// sent before panicking is kept; one it never sent is shown as no status, as the log shows a
// missing one.
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

// With the request log off, a handler that returns normally with a 200 reply writes no file.
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
