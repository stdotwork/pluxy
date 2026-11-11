package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Upstream capture: with request-log on, a request's log also holds what the extension sent
// upstream and what came back, one pair per attempt, without secrets; with it off nothing is
// captured or sent over the extension socket.

// Fake upstream keys for the capture test, one per account.
const (
	captureKeyA = "sk-capture-a-e2e-51c9d0"
	captureKeyB = "sk-capture-b-e2e-8e27aa"
)

// captureUpstream is a fake provider that records the bodies it gets. Refusing, it answers 429
// with a body and a header that repeat the Authorization and X-Gateway-Key values it got, as a
// gateway's error might; otherwise it answers captureOK, or a long stream when asked to stream,
// whose first chunk repeats the key it got across the end of what a log keeps of a body's start.
type captureUpstream struct {
	*httptest.Server
	refuse bool
	gate   chan struct{} // closed by a test to let a "gate-stream" reply go on after its first event
	mu     sync.Mutex
	bodies []string
}

const captureOK = `{"id":"chatcmpl-cap","object":"chat.completion","created":1700000003,"model":"gpt-cap",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"captured reply"},"finish_reason":"stop"}]}`

// captureChunks is how many chunks a streamed reply has: about 3 MiB in all, more than a request
// log keeps of a reply on either side.
const captureChunks = 20000

func captureChunk(i int) string {
	return fmt.Sprintf(`data: {"id":"chatcmpl-big","object":"chat.completion.chunk","created":1,"model":"gpt-cap",`+
		`"choices":[{"index":0,"delta":{"content":"m-%05d "},"finish_reason":null}]}`+"\n\n", i)
}

func newCaptureUpstream(t *testing.T, refuse bool) *captureUpstream {
	u := &captureUpstream{refuse: refuse, gate: make(chan struct{})}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(b))
		u.mu.Unlock()
		w.Header().Set("X-Echo", r.Header.Get("X-Gateway-Key"))
		switch {
		case u.refuse:
			msg, _ := json.Marshal("rate limited for " + r.Header.Get("Authorization") + " and " + r.Header.Get("X-Gateway-Key"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":`+string(msg)+`,"type":"rate_limit","code":"rate_limit_exceeded"}}`)
		case strings.Contains(string(b), "gate-stream"):
			// The first event, then nothing until the test has read it at the client.
			w.Header().Set("Content-Type", "text/event-stream")
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			event := func(content, finish string) string {
				return `data: {"id":"chatcmpl-gate","object":"chat.completion.chunk","created":1,"model":"gpt-cap",` +
					`"choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":` + finish + `}]}` + "\n\n"
			}
			io.WriteString(w, event("first-event "+key+" visible", "null"))
			w.(http.Flusher).Flush()
			select {
			case <-u.gate:
			case <-time.After(10 * time.Second):
			}
			io.WriteString(w, event("second-event", "null")+event("", `"stop"`)+"data: [DONE]\n\n")
		case strings.Contains(string(b), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			first := `data: {"id":"chatcmpl-big","object":"chat.completion.chunk","created":1,"model":"gpt-cap","choices":[{"index":0,"delta":{"content":"`
			// The key starts 8 bytes before the protocol's bound on a body's start, 32 KiB.
			first += strings.Repeat("x", 32<<10-8-len(first)) + key + `"},"finish_reason":null}]}` + "\n\n"
			io.WriteString(w, first)
			for i := range captureChunks {
				io.WriteString(w, captureChunk(i))
			}
			io.WriteString(w, `data: {"id":"chatcmpl-big","object":"chat.completion.chunk","created":1,"model":"gpt-cap","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, captureOK)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *captureUpstream) received() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies...)
}

// tapPackage writes a package folder whose program is frametap running the extension program
// inner (a path under binDir) as id, and returns its manifest's path and a reader of the frames
// that crossed its socket so far, as frametap writes them: "<connection> core|ext <what>".
func tapPackage(t *testing.T, id, inner string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	for name, from := range map[string]string{"frametap": filepath.Join(binDir, "frametap"), "inner": inner} {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(map[string]any{"manifest_version": 0, "id": id, "version": "0.1.0", "protocol": []int{0},
		"programs": map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "frametap"}})
	manifest := filepath.Join(dir, "pluxy-extension.json")
	if err := os.WriteFile(manifest, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest, func() []string {
		b, _ := os.ReadFile(filepath.Join(dir, "frames.log"))
		if len(b) == 0 {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// sections returns the text of each section of a request log with the given title, up to the
// next section.
func sections(text, title string) []string {
	parts := strings.Split(text, "=== "+title+" ===\n")
	var out []string
	for _, p := range parts[1:] {
		if i := strings.Index(p, "\n=== "); i >= 0 {
			p = p[:i]
		}
		out = append(out, p)
	}
	return out
}

func TestUpstreamCapture(t *testing.T) {
	refusing, serving := newCaptureUpstream(t, true), newCaptureUpstream(t, false)
	proxy := newHTTPProxy(t, true)
	manifest, frames := tapPackage(t, "pluxy/openai-compatible", filepath.Join(binDir, "openai-compatible"))
	headers := map[string]string{"X-Gateway-Key": gatewayKey, "X-Plain": "plain-header-value"}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing":        map[string]any{"strategy": "fill-first"},
		"extension": map[string]any{
			"manifest": manifest,
			"accounts": []any{
				map[string]any{"id": "cap-a", "headers": headers, "settings": map[string]any{"base_url": refusing.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": captureKeyA}},
				// The serving account is reached through a proxy with a password.
				map[string]any{"id": "cap-b", "headers": headers, "proxy_url": proxy.URL("http"),
					"settings":   map[string]any{"base_url": "http://" + net.JoinHostPort(proxiedHost, port(t, serving.URL)) + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": captureKeyB}},
			},
		},
	})
	os.WriteFile(cfgPath, b, 0o600)
	base, _ := runCoreFile(t, cfgPath, noWatch...)
	c := &mcore{base: base, path: cfgPath}
	secrets := []string{captureKeyA, captureKeyB, gatewayKey, proxyPassword, proxyUser + ":" + proxyPassword, clientKey, managementKey}
	send := func(pin, body string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+clientKey)
		if pin != "" {
			req.Header.Set("X-Pluxy-Account", pin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		reply, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("X-CPA-TRACE-ID"), string(reply)
	}
	// A refusal holds its model back on the account for a while, so each step asks for another.
	plain := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"capture me"}]}`
	}
	since := func(n int) []string { return frames()[n:] }
	// byCall groups frame lines by connection, without the connection number, in order.
	byCall := func(lines []string) [][]string {
		var order []string
		calls := map[string][]string{}
		for _, l := range lines {
			n, rest, _ := strings.Cut(l, " ")
			if calls[n] == nil {
				order = append(order, n)
			}
			calls[n] = append(calls[n], rest)
		}
		out := make([][]string, len(order))
		for i, n := range order {
			out[i] = calls[n]
		}
		return out
	}

	// With request-log off, a failed request's error log has no upstream sections, and no call
	// asks for capture or gets a diagnostic frame back.
	start := len(frames())
	if code, _, reply := send("cap-a", plain("gpt-cap-1")); code != http.StatusTooManyRequests {
		t.Fatalf("pinned to the refusing account: %d %s", code, reply)
	}
	if code, _, reply := send("", plain("gpt-cap-2")); code != http.StatusOK {
		t.Fatalf("with a retry: %d %s", code, reply)
	}
	l := c.errorLogs(t)
	if len(l.Files) != 1 {
		t.Fatalf("error logs: %+v", l)
	}
	text := c.download(t, "/request-error-logs/"+l.Files[0].Name)
	if !strings.Contains(text, "Status: 429") || strings.Contains(text, "=== UPSTREAM") {
		t.Errorf("error log with request-log off:\n%s", text)
	}
	off := since(start)
	generates := 0
	for _, call := range byCall(off) {
		for _, f := range call {
			if f == "core generate capture=false" {
				generates++
			}
			if strings.Contains(f, "capture=true") || f == "ext diagnostic" {
				t.Errorf("with request-log off, a frame %q crossed the socket", f)
			}
		}
	}
	if generates != 3 {
		t.Errorf("with request-log off: %d generate calls, want 3; frames %q", generates, off)
	}

	// With it on, the log holds one pair per attempt: the refused one, then the retry.
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"request-log": true}}}`)
	start = len(frames())
	bodiesA, bodiesB := len(refusing.received()), len(serving.received())
	code, trace, reply := send("", plain("gpt-cap-3"))
	if code != http.StatusOK || trace == "" || !strings.Contains(reply, "captured reply") {
		t.Fatalf("logged request: %d %q %s", code, trace, reply)
	}
	text = c.download(t, "/request-log-by-id/"+trace)
	wantNoSecrets(t, "the request log", text, secrets...)
	reqs, resps := sections(text, "UPSTREAM REQUEST"), sections(text, "UPSTREAM RESPONSE")
	if len(reqs) != 2 || len(resps) != 2 {
		t.Fatalf("%d upstream requests and %d responses logged, want 2 and 2:\n%s", len(reqs), len(resps), text)
	}
	sentA, sentB := refusing.received()[bodiesA:], serving.received()[bodiesB:]
	if len(sentA) != 1 || len(sentB) != 1 {
		t.Fatalf("the upstreams got %d and %d requests, want 1 and 1", len(sentA), len(sentB))
	}
	// What the upstreams got is the client's request, whatever the log holds of it.
	for _, got := range []string{sentA[0], sentB[0]} {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(got), &req); err != nil || req.Model != "gpt-cap-3" ||
			len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "capture me" {
			t.Errorf("an upstream got %q (%v), want model gpt-cap-3 and one user message \"capture me\"", got, err)
		}
	}
	for i, want := range []struct {
		account, url, body, status, reply string
	}{
		{"cap-a", refusing.URL + "/v1/chat/completions", sentA[0], "Status: 429", ""},
		{"cap-b", "http://" + net.JoinHostPort(proxiedHost, port(t, serving.URL)) + "/v1/chat/completions", sentB[0], "Status: 200", captureOK},
	} {
		for _, s := range []string{"Account: " + want.account, "URL: " + want.url, "Method: POST", "Authorization: Bearer [redacted]",
			"X-Gateway-Key: [redacted]", "X-Plain: plain-header-value", want.body} {
			if !strings.Contains(reqs[i], s) {
				t.Errorf("upstream request %d lacks %q:\n%s", i+1, s, reqs[i])
			}
		}
		for _, s := range []string{want.status, want.reply} {
			if !strings.Contains(resps[i], s) {
				t.Errorf("upstream response %d lacks %q:\n%s", i+1, s, resps[i])
			}
		}
	}
	if !strings.Contains(resps[0], "rate limited for") {
		t.Errorf("the refusal's body is not logged:\n%s", resps[0])
	}
	if i := strings.Index(text, "=== UPSTREAM REQUEST ==="); i < strings.Index(text, "=== REQUEST BODY ===") || i > strings.Index(text, "=== RESPONSE ===") {
		t.Errorf("upstream sections are not between the client's request and its reply:\n%s", text)
	}
	// Each call asked for capture and sent its diagnostic frame just before its terminal event.
	on := since(start)
	calls, diagnostics := 0, 0
	for _, call := range byCall(on) {
		if call[0] != "core generate capture=true" {
			continue
		}
		calls++
		for i, f := range call {
			if f != "ext diagnostic" {
				continue
			}
			diagnostics++
			if i+1 == len(call) || call[i+1] != "ext error" && call[i+1] != "ext finish" {
				t.Errorf("a diagnostic frame not followed by the terminal event: %q", call)
			}
		}
	}
	if calls != 2 || diagnostics != 2 {
		t.Errorf("with request-log on: %d captured calls and %d diagnostic frames, want 2 and 2; frames %q", calls, diagnostics, on)
	}

	// A streamed reply longer than the bounds keeps its start and its end only.
	code, trace, reply = send("cap-b", `{"model":"gpt-cap","stream":true,"messages":[{"role":"user","content":"stream it"}]}`)
	if code != http.StatusOK || !strings.Contains(reply, fmt.Sprintf("m-%05d", captureChunks-1)) {
		t.Fatalf("streamed request: %d %q", code, trace)
	}
	text = c.download(t, "/request-log-by-id/"+trace)
	if len(text) >= 2<<20 {
		t.Errorf("the streamed request's log is %d bytes, want under 2 MiB", len(text))
	}
	resps = sections(text, "UPSTREAM RESPONSE")
	if len(resps) != 1 {
		t.Fatalf("%d upstream responses logged, want 1:\n%.4000s", len(resps), text)
	}
	r := resps[0]
	if !strings.Contains(r, `data: {"id":"chatcmpl-big"`) ||
		!strings.Contains(r, "Content-Type: text/event-stream") || !strings.Contains(r, captureChunk(captureChunks-1)) ||
		!strings.Contains(r, "data: [DONE]") || !strings.Contains(r, " bytes not logged ...]") ||
		strings.Contains(r, fmt.Sprintf("m-%05d", captureChunks/2)) {
		t.Errorf("the streamed upstream response is not its start and end:\n%.2000s\n...\n%s", r, r[max(0, len(r)-2000):])
	}
	if len(r) > 200<<10 {
		t.Errorf("the streamed upstream response's section is %d bytes", len(r))
	}
	// The key cut in two by the left-out middle is not kept in part either.
	wantNoSecrets(t, "the streamed request's log", text, append(secrets, captureKeyB[:12])...)

	// With capture on, a streamed reply still reaches the client as the upstream sends it, in
	// order and to a clean end: the upstream sends its first event and holds the rest until the
	// client has read that one, so a reply held back for recording could not get through.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-cap","stream":true,"messages":[{"role":"user","content":"gate-stream"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("X-Pluxy-Account", "cap-b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	trace = resp.Header.Get("X-CPA-TRACE-ID")
	br := bufio.NewReader(resp.Body)
	var got strings.Builder
	firstRead := make(chan error, 1)
	go func() {
		for !strings.Contains(got.String(), "first-event") {
			line, err := br.ReadString('\n')
			got.WriteString(line)
			if err != nil {
				firstRead <- err
				return
			}
		}
		firstRead <- nil
	}()
	select {
	case err := <-firstRead:
		if err != nil {
			t.Fatalf("reading the first event: %v; got %q", err, got.String())
		}
	case <-time.After(8 * time.Second):
		close(serving.gate)
		t.Fatal("the first event did not reach the client while the upstream held the rest back")
	}
	close(serving.gate)
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v", err)
	}
	streamed := got.String() + string(rest)
	last := -1
	for _, want := range []string{"first-event", "second-event", `"finish_reason":"stop"`, "data: [DONE]"} {
		i := strings.Index(streamed, want)
		if i <= last {
			t.Errorf("%q is missing or out of order (at %d, after %d) in the stream:\n%s", want, i, last, streamed)
		}
		last = max(last, i)
	}
	text = c.download(t, "/request-log-by-id/"+trace)
	wantNoSecrets(t, "the gated stream's log", text, secrets...)
	resps = sections(text, "UPSTREAM RESPONSE")
	// The reply was short: all of it is logged, with the key replaced and what follows it kept.
	if len(resps) != 1 || !strings.Contains(resps[0], "first-event [redacted] visible") ||
		strings.Index(resps[0], "first-event") > strings.Index(resps[0], "second-event") ||
		!strings.Contains(resps[0], "data: [DONE]") || strings.Contains(resps[0], "not logged") {
		t.Errorf("the gated stream's upstream response is not logged whole:\n%v", resps)
	}
}
