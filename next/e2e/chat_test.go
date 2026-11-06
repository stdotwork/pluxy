// Package e2e drives the built pluxy binary over HTTP the way a client does, with the
// openai-compatible provider extension in front of a fake upstream server.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	clientKey   = "client-key-e2e"
	upstreamKey = "sk-upstream-e2e"
)

var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pluxy-next-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binDir = dir
	code := 1
	if err := build(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

// build compiles core and the extensions and writes each extension's manifest next to it.
func build(dir string) error {
	for _, b := range []struct{ out, pkg string }{
		{"pluxy", "../cmd/pluxy"},
		{"openai-compatible", "../extensions/openai-compatible"},
		{filepath.Join("oauth", "oauth"), "../examples/oauth"},
		{filepath.Join("codex", "codex"), "../extensions/codex"},
		{filepath.Join("claude", "claude"), "../extensions/claude"},
		{"testext", "./testext"},
	} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, b.out), b.pkg)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building %s: %w", b.pkg, err)
		}
	}
	manifest := map[string]any{
		"manifest_version": 0,
		"id":               "pluxy/openai-compatible",
		"version":          "0.1.0",
		"protocol":         []int{0},
		"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "openai-compatible"},
	}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "pluxy-extension.json"), b, 0o644); err != nil {
		return err
	}
	for id, name := range map[string]string{"example/oauth": "oauth", "pluxy/codex": "codex", "pluxy/claude": "claude"} {
		manifest["id"] = id
		manifest["programs"] = map[string]string{runtime.GOOS + "/" + runtime.GOARCH: name}
		b, _ = json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, name, "pluxy-extension.json"), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// upstream is a fake OpenAI-compatible server that records what it receives.
type upstream struct {
	*httptest.Server
	mu        sync.Mutex
	bodies    [][]byte
	auth      []string
	release   chan struct{} // the streamed reply pauses after its first text chunk until closed
	cancelled chan struct{} // closed when a streamed request's context ends early
	respond   func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{release: make(chan struct{}), cancelled: make(chan struct{})}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.auth = append(u.auth, r.Header.Get("Authorization"))
		u.mu.Unlock()
		u.respond(w, r, body)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) received() ([][]byte, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.bodies...), append([]string(nil), u.auth...)
}

// streamReply answers like OpenAI's streaming Chat Completions with usage included.
func (u *upstream) streamReply(w http.ResponseWriter, r *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	chunk := func(s string) {
		fmt.Fprintf(w, "data: %s\n\n", s)
		f.Flush()
	}
	const head = `"id":"chatcmpl-up1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream"`
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`)
	select {
	case <-u.release:
	case <-r.Context().Done():
		close(u.cancelled)
		return
	case <-time.After(10 * time.Second):
		return
	}
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"content":", "},"finish_reason":null}]}`)
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"content":"world!"},"finish_reason":null}]}`)
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	chunk(`{` + head + `,"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`)
	chunk(`[DONE]`)
}

// startCore runs the pluxy binary against u and returns its base URL. Each option may change
// the config before core starts.
func startCore(t *testing.T, u *upstream, options ...func(cfg map[string]any)) string {
	t.Helper()
	base, _ := startCoreLogged(t, u, options...)
	return base
}

// coreLog collects the lines core writes to stderr.
type coreLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *coreLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

// waitFor returns core's log once a line contains s, failing the test if none does in time.
func (l *coreLog) waitFor(t *testing.T, s string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		all := strings.Join(l.lines, "\n")
		l.mu.Unlock()
		if strings.Contains(all, s) {
			return all
		}
		if time.Now().After(deadline) {
			t.Fatalf("core logged no line containing %q; log:\n%s", s, all)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startCoreLogged is startCore that also returns core's log.
func startCoreLogged(t *testing.T, u *upstream, options ...func(cfg map[string]any)) (string, *coreLog) {
	t.Helper()
	cfg := map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "upstream-1",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	}
	for _, o := range options {
		o(cfg)
	}
	return runCoreLogged(t, cfg)
}

// runCore runs the pluxy binary with config cfg and returns its base URL.
func runCore(t *testing.T, cfg map[string]any) string {
	t.Helper()
	base, _ := runCoreLogged(t, cfg)
	return base
}

// runCoreLogged is runCore that also returns core's log.
func runCoreLogged(t *testing.T, cfg map[string]any) (string, *coreLog) {
	t.Helper()
	base, _, logs := runCoreArgs(t, cfg)
	return base, logs
}

// runCoreArgs runs the pluxy binary with config cfg and extra command-line arguments, and
// returns its base URL, the path of its config file and its log.
func runCoreArgs(t *testing.T, cfg map[string]any, args ...string) (string, string, *coreLog) {
	t.Helper()
	logs := &coreLog{}
	dir := t.TempDir()
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), append([]string{"-config", path}, args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	addr := make(chan string, 1)
	go func() {
		defer close(addr) // core exited
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			t.Log("core:", line)
			logs.add(line)
			if rest, ok := strings.CutPrefix(line, "pluxy: listening on "); ok {
				select {
				case addr <- rest:
				default:
				}
			}
		}
	}()
	select {
	case a, ok := <-addr:
		if !ok {
			t.Fatal("core exited before listening")
		}
		return a, path, logs
	case <-time.After(15 * time.Second):
		t.Fatal("core did not report its listening address")
		return "", "", nil
	}
}

func post(t *testing.T, ctx context.Context, base, key, body string) *http.Response {
	t.Helper()
	return postPath(t, ctx, base, "/v1/chat/completions", key, body)
}

func postPath(t *testing.T, ctx context.Context, base, path, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type chunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// readData returns the payload of the next SSE data line.
func readData(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended early: %v", err)
		}
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: "); ok {
			return data
		}
	}
}

// The slice's main check: a streamed reply through core from a fake upstream, arriving
// incrementally, with the request passed through untouched and the account's key used upstream.
func TestStreamingChatThroughExtension(t *testing.T) {
	u := newUpstream(t)
	u.respond = u.streamReply
	base := startCore(t, u)

	body := `{"model":"gpt-test","stream":true,"stream_options":{"include_usage":true},` +
		`"messages":[{"role":"user","content":"Say hello"}],"x_vendor_field":{"keep":true}}`
	resp := post(t, context.Background(), base, clientKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	r := bufio.NewReader(resp.Body)

	var text strings.Builder
	var sawRole bool
	var finish string
	var usage *chunk
	// Read until "Hello" arrives; the upstream is still holding the rest of the reply.
	for !strings.Contains(text.String(), "Hello") {
		var c chunk
		if err := json.Unmarshal([]byte(readData(t, r)), &c); err != nil {
			t.Fatal(err)
		}
		if c.ID != "chatcmpl-up1" || c.Object != "chat.completion.chunk" || c.Model != "gpt-test-upstream" {
			t.Fatalf("chunk header = %q %q %q", c.ID, c.Object, c.Model)
		}
		for _, ch := range c.Choices {
			sawRole = sawRole || ch.Delta.Role == "assistant"
			text.WriteString(ch.Delta.Content)
		}
	}
	close(u.release)
	for {
		data := readData(t, r)
		if data == "[DONE]" {
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatal(err)
		}
		for _, ch := range c.Choices {
			text.WriteString(ch.Delta.Content)
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
		if c.Usage != nil {
			usage = &c
		}
	}
	if !sawRole {
		t.Error("no chunk carried role assistant")
	}
	if got := text.String(); got != "Hello, world!" {
		t.Errorf("text = %q, want %q", got, "Hello, world!")
	}
	if finish != "stop" {
		t.Errorf("finish_reason = %q, want stop", finish)
	}
	if usage == nil || len(usage.Choices) != 0 || usage.Usage.PromptTokens != 9 || usage.Usage.CompletionTokens != 3 || usage.Usage.TotalTokens != 12 {
		t.Errorf("usage chunk = %+v, want prompt 9, completion 3, total 12 with no choices", usage)
	}

	bodies, auth := u.received()
	if len(bodies) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(bodies))
	}
	if string(bodies[0]) != body {
		t.Errorf("upstream body changed:\n got %s\nwant %s", bodies[0], body)
	}
	if auth[0] != "Bearer "+upstreamKey {
		t.Errorf("upstream Authorization = %q, want the account's key", auth[0])
	}
}

func TestNonStreamingChat(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up2","object":"chat.completion","created":1700000001,"model":"gpt-test-upstream",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"Hi there."},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}
	base := startCore(t, u)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, context.Background(), base, clientKey, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if got.ID != "chatcmpl-up2" || got.Object != "chat.completion" || got.Model != "gpt-test-upstream" {
		t.Errorf("header = %q %q %q", got.ID, got.Object, got.Model)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Role != "assistant" || got.Choices[0].Message.Content != "Hi there." || got.Choices[0].FinishReason != "length" {
		t.Errorf("choices = %+v", got.Choices)
	}
	if got.Usage.PromptTokens != 4 || got.Usage.CompletionTokens != 2 || got.Usage.TotalTokens != 6 {
		t.Errorf("usage = %+v", got.Usage)
	}
	bodies, _ := u.received()
	if len(bodies) != 1 || string(bodies[0]) != body {
		t.Errorf("upstream bodies = %q, want the client's body unchanged", bodies)
	}
}

func TestUpstreamRateLimitBecomesClientError(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
	}
	base := startCore(t, u)
	resp := post(t, context.Background(), base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", resp.StatusCode, raw)
	}
	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if got.Error.Message != "Rate limit reached" || got.Error.Type != "rate_limit_error" || got.Error.Code != "rate_limit_exceeded" {
		t.Errorf("error = %+v", got.Error)
	}
}

func TestClientKeyRequired(t *testing.T) {
	u := newUpstream(t)
	u.respond = u.streamReply
	base := startCore(t, u)
	for _, key := range []string{"", "wrong-key"} {
		resp := post(t, context.Background(), base, key, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("key %q: status %d, want 401", key, resp.StatusCode)
		}
	}
	if bodies, _ := u.received(); len(bodies) != 0 {
		t.Errorf("upstream got %d requests from unauthenticated clients", len(bodies))
	}
}

// A client that goes away mid-stream cancels the upstream request.
func TestClientDisconnectCancelsUpstream(t *testing.T) {
	u := newUpstream(t)
	u.respond = u.streamReply
	base := startCore(t, u)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	r := bufio.NewReader(resp.Body)
	for data := ""; !strings.Contains(data, "Hello"); {
		data = readData(t, r)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-u.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not cancelled after the client disconnected")
	}
}

// A non-streamed request whose upstream answers with a stream larger than core will collect
// gets a clear error, and core cancels the upstream request instead of reading it all. Chat,
// Responses, Messages and Gemini clients collect the reply in different aggregators; all are
// bounded.
func TestNonStreamingReplyIsBounded(t *testing.T) {
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"gpt-test","input":"hi"}`},
		{"/v1/messages", `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1beta/models/gpt-test:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	} {
		path, body := c.path, c.body
		t.Run(path, func(t *testing.T) {
			const total = 128 << 20 // twice core's 64 MiB limit
			u := newUpstream(t)
			u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "text/event-stream")
				const head = `"id":"chatcmpl-big","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream"`
				content := strings.Repeat("x", 64<<10)
				fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n", head)
				for sent := 0; sent < total; sent += len(content) {
					if _, err := fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", head, content); err != nil || r.Context().Err() != nil {
						close(u.cancelled)
						return
					}
				}
				fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", head)
			}
			base := startCore(t, u)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			resp := postPath(t, ctx, base, path, clientKey, body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status %d, want 502; body starts %.200s", resp.StatusCode, raw)
			}
			var got struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("%v: %s", err, raw)
			}
			if !strings.Contains(got.Error.Message, "64 MiB") || !strings.Contains(got.Error.Message, "stream") {
				t.Errorf("error message = %q, want it to name the 64 MiB limit and streaming", got.Error.Message)
			}
			select {
			case <-u.cancelled:
			case <-time.After(10 * time.Second):
				t.Fatal("the upstream request was not cancelled after the limit was reached")
			}
		})
	}
}
