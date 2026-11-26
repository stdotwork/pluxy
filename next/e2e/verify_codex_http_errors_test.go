package e2e

// What a Codex CLI gets from core on its own path (/backend-api/codex/responses) when the
// upstream refuses or breaks the turn: a status and an error body the client can act on, never a
// hang and never a 200 with an empty stream. The Codex CLI opens this route when its
// chatgpt_base_url points at core, and it reads the same OpenAI error object on HTTP and over the
// Responses WebSocket.
//
// The test follows the bug class cc-codex-http-errors-through-proxy (CLIProxyAPI issues #2401,
// #286, #3017, #216: Codex CLI gets 401/404/502/503 or a connection error through the proxy).
// The upstream replies are shaped like ChatGPT's own error objects, and the 502 is an HTML
// gateway page, as a reverse proxy in front of the provider sends.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// codexErrorUpstream is a fake Codex upstream whose Responses endpoint answers every POST with
// one scripted failure.
type codexErrorUpstream struct {
	*httptest.Server

	status   int    // status to answer with; 0 drops the connection without a reply
	body     string // body to answer with
	retry    string // Retry-After header, when set
	dropOpen bool   // answer 200 text/event-stream, then close before the first event

	mu   sync.Mutex
	hits int
	path string
	auth string
}

func newCodexErrorUpstream(t *testing.T, u *codexErrorUpstream) *codexErrorUpstream {
	t.Helper()
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/codex/responses" {
			http.NotFound(w, r)
			return
		}
		u.mu.Lock()
		u.hits++
		u.path, u.auth = r.URL.Path, r.Header.Get("Authorization")
		u.mu.Unlock()
		if u.status == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if u.dropOpen {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			return
		}
		if u.retry != "" {
			w.Header().Set("Retry-After", u.retry)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.status)
		_, _ = io.WriteString(w, u.body)
	}))
	t.Cleanup(u.Server.Close)
	return u
}

// startCodexErrorCore runs core with one Codex account whose Responses endpoint is upstream.
func startCodexErrorCore(t *testing.T, upstream string) string {
	t.Helper()
	return runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "codex-errors",
				"models":     []string{"gpt-5.5-codex"},
				"settings":   map[string]any{"base_url": upstream + "/backend-api/codex"},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-9f3c2b7a1d4e6f80"},
			}},
		},
	})
}

// codexCLIHeaders are the headers the Codex CLI sends on its requests.
func codexCLIHeaders() map[string]string {
	return map[string]string{
		"User-Agent": "codex_cli_rs/0.150.0 (Linux; x86_64)",
		"Originator": "codex_cli_rs",
		"Version":    "0.150.0",
		"Session_id": "s-codex-errors",
	}
}

const codexErrorRequestBody = `{"model":"gpt-5.5-codex","instructions":"You are a coding agent.",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say hello"}]}],` +
	`"stream":true}`

// codexErrorReply is one client reply, read whole.
type codexErrorReply struct {
	status      int
	contentType string
	retryAfter  string
	body        []byte
	elapsed     time.Duration
}

// postCodexCLI posts the Codex CLI's request to core's Codex route.
func postCodexCLI(t *testing.T, base string) codexErrorReply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/backend-api/codex/responses",
		strings.NewReader(codexErrorRequestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range codexCLIHeaders() {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the request got no reply: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	return codexErrorReply{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"),
		retryAfter: resp.Header.Get("Retry-After"), body: body, elapsed: time.Since(start)}
}

// checkCodexErrorReply asserts a reply a Codex CLI can act on: the expected status, an OpenAI
// error object with a message, a type and a code, and no hang.
func checkCodexErrorReply(t *testing.T, what string, r codexErrorReply, wantStatus int) {
	t.Helper()
	if r.elapsed > 15*time.Second {
		t.Errorf("%s: the reply took %s, a client would have timed out", what, r.elapsed)
	}
	if !strings.HasPrefix(r.contentType, "application/json") {
		t.Errorf("%s: Content-Type %q, want application/json: %s", what, r.contentType, r.body)
	}
	if r.status != wantStatus {
		t.Errorf("%s: status %d, want %d: %s", what, r.status, wantStatus, r.body)
	}
	if r.status < 400 || r.status > 599 {
		t.Errorf("%s: status %d is not an error a client retries or reports: %s", what, r.status, r.body)
	}
	var b struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &b); err != nil {
		t.Fatalf("%s: the reply is not JSON (%v): %s", what, err, r.body)
	}
	if b.Error.Message == "" || b.Error.Type == "" || b.Error.Code == "" {
		t.Errorf("%s: error object has empty message, type or code: %s", what, r.body)
	}
}

// TestVerifyCodexHTTPErrorStatusesThroughProxy sends the Codex CLI's request to core's Codex
// route while the upstream answers 401, 404, 429, 502 or 503, and checks the client gets that
// status back in an error body it can act on (the Codex CLI refreshes auth on 401, reports a
// missing model on 404, backs off on 429, and retries 5xx).
func TestVerifyCodexHTTPErrorStatusesThroughProxy(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		retry  string
	}{
		{"401 expired token", http.StatusUnauthorized,
			`{"error":{"message":"Your access token could not be refreshed because your refresh token was revoked.","type":"invalid_request_error","code":"token_expired"}}`, ""},
		{"404 unknown model", http.StatusNotFound,
			`{"error":{"message":"The model gpt-5.5-codex does not exist or you do not have access to it.","type":"invalid_request_error","code":"model_not_found"}}`, ""},
		{"429 rate limited", http.StatusTooManyRequests,
			`{"error":{"message":"Rate limit reached for gpt-5.5-codex. Please try again in 20s.","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, "20"},
		{"502 gateway page", http.StatusBadGateway,
			`<html><head><title>502 Bad Gateway</title></head><body><h1>502 Bad Gateway</h1></body></html>`, ""},
		{"503 service unavailable", http.StatusServiceUnavailable,
			`{"error":{"message":"Service Unavailable","type":"server_error","code":"server_error"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newCodexErrorUpstream(t, &codexErrorUpstream{status: tc.status, body: tc.body, retry: tc.retry})
			base := startCodexErrorCore(t, up.URL)
			r := postCodexCLI(t, base)
			checkCodexErrorReply(t, tc.name, r, tc.status)
			up.mu.Lock()
			hits, path, auth := up.hits, up.path, up.auth
			up.mu.Unlock()
			if hits != 1 || path != "/backend-api/codex/responses" || auth != "Bearer sk-9f3c2b7a1d4e6f80" {
				t.Errorf("%s: upstream got %d request(s) on %q with %q, want one on /backend-api/codex/responses with the account's token",
					tc.name, hits, path, auth)
			}
			if tc.status == http.StatusTooManyRequests && r.retryAfter != tc.retry {
				t.Errorf("%s: Retry-After %q, want %q", tc.name, r.retryAfter, tc.retry)
			}
		})
	}
}

// TestVerifyCodexUpstreamConnectionFailureThroughProxy breaks the connection in each way a
// network fault does (refused, dropped before the reply, 200 headers then an empty stream) and
// checks the Codex CLI gets an error reply, never a hang or a 200 with an empty stream.
func TestVerifyCodexUpstreamConnectionFailureThroughProxy(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		// A listener closed before the request: the port is free and nothing answers.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		base := startCodexErrorCore(t, "http://"+addr)
		r := postCodexCLI(t, base)
		checkCodexErrorReply(t, "connection refused", r, http.StatusServiceUnavailable)
		if !strings.Contains(string(r.body), "connection refused") {
			t.Errorf("the error does not say the upstream could not be reached: %s", r.body)
		}
	})
	t.Run("dropped before the reply", func(t *testing.T) {
		up := newCodexErrorUpstream(t, &codexErrorUpstream{status: 0})
		base := startCodexErrorCore(t, up.URL)
		r := postCodexCLI(t, base)
		checkCodexErrorReply(t, "dropped before the reply", r, http.StatusServiceUnavailable)
	})
	t.Run("200 then an empty stream", func(t *testing.T) {
		up := newCodexErrorUpstream(t, &codexErrorUpstream{status: http.StatusOK, dropOpen: true})
		base := startCodexErrorCore(t, up.URL)
		r := postCodexCLI(t, base)
		t.Logf("empty stream: status %d, body %s", r.status, r.body)
		// The upstream opened the reply and sent no event: the client must not read 200 with an
		// empty stream as a finished turn, and the failure is the upstream's (a 5xx it retries).
		if r.status < 500 || r.status > 599 {
			t.Fatalf("a stream that ended before its first event reached the client as status %d: %q", r.status, r.body)
		}
		checkCodexErrorReply(t, "200 then an empty stream", r, r.status)
	})
}

// dialCodexCLI opens the Codex CLI's own WebSocket route on core.
func dialCodexCLI(t *testing.T, base string) (*websocket.Conn, context.Context) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + clientKey}}
	for k, v := range codexCLIHeaders() {
		h.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/backend-api/codex/responses",
		&websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("opening the WebSocket: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	msg, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.5-codex",
		"input": []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "Say hello"}}}}})
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatalf("sending response.create: %v", err)
	}
	return c, ctx
}

// TestVerifyCodexWebSocketErrorThroughProxy sends a response.create over the Codex CLI's own
// WebSocket route while the upstream refuses the turn. A request the upstream found at fault is
// reported to the client with its status and message (the client fixes the request); any other
// failure ends the socket rather than leaving the turn hanging with no reply.
func TestVerifyCodexWebSocketErrorThroughProxy(t *testing.T) {
	t.Run("upstream refuses the request", func(t *testing.T) {
		up := newCodexErrorUpstream(t, &codexErrorUpstream{status: http.StatusBadRequest,
			body: `{"error":{"message":"Invalid value for input.","type":"invalid_request_error","code":"invalid_value"}}`})
		c, ctx := dialCodexCLI(t, startCodexErrorCore(t, up.URL))
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("the socket closed without reporting the refused request: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("message %q: %v", data, err)
			}
			if m["type"] != "error" {
				continue
			}
			if status, _ := m["status"].(float64); int(status) != http.StatusBadRequest {
				t.Errorf("error status %v, want 400: %s", m["status"], data)
			}
			e, _ := m["error"].(map[string]any)
			if msg, _ := e["message"].(string); msg == "" {
				t.Errorf("error message is empty: %s", data)
			}
			return
		}
	})
	t.Run("upstream unavailable", func(t *testing.T) {
		up := newCodexErrorUpstream(t, &codexErrorUpstream{status: http.StatusServiceUnavailable,
			body: `{"error":{"message":"Service Unavailable","type":"server_error","code":"server_error"}}`})
		c, ctx := dialCodexCLI(t, startCodexErrorCore(t, up.URL))
		start := time.Now()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("the turn hung for %s after the upstream refused it", time.Since(start))
				}
				t.Logf("the socket ended after %s: %v", time.Since(start), err)
				return
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("message %q: %v", data, err)
			}
			if m["type"] == "response.completed" || m["type"] == "response.failed" {
				t.Fatalf("the failed turn reached the client as %v", m["type"])
			}
		}
	})
}
