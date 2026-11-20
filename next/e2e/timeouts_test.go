package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func startTimeoutCore(t *testing.T, u *upstream, routing map[string]any, accounts ...map[string]any) (string, *coreLog) {
	t.Helper()
	var accts []any
	for _, a := range accounts {
		accts = append(accts, a)
	}
	return runCoreLogged(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing":        routing,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": accts,
		},
	})
}

func timeoutAccount(u *upstream, id, key string, timeouts map[string]any) map[string]any {
	a := map[string]any{
		"id":         id,
		"settings":   map[string]any{"base_url": u.URL + "/v1"},
		"credential": map[string]any{"kind": "api_key", "api_key": key},
	}
	if timeouts != nil {
		a["timeouts"] = timeouts
	}
	return a
}

func hasTimeoutLog(logs *coreLog, parts ...string) bool {
	deadline := time.Now().Add(time.Second)
	for {
		logs.mu.Lock()
		for _, line := range logs.lines {
			if !strings.Contains(line, "upstream timeout:") {
				continue
			}
			ok := true
			for _, p := range parts {
				if !strings.Contains(line, p) {
					ok = false
					break
				}
			}
			if ok {
				logs.mu.Unlock()
				return true
			}
		}
		logs.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 1. An upstream that accepts and sends no headers rotates to the second account within the
// header limit plus a margin, and never hangs; when all accounts hang on headers, the request
// fails within the combined header limits rather than hanging.
func TestUpstreamTimeoutHeaderRotates(t *testing.T) {
	t.Run("rotates to second account", func(t *testing.T) {
		u := newUpstream(t)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if keyOf(r) == "sk-secret-a" {
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			const head = `"id":"chatcmpl-b","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test"`
			io.WriteString(w, `data: {`+head+`,"choices":[{"index":0,"delta":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			f.Flush()
		}
		base, logs := startTimeoutCore(t, u,
			map[string]any{"timeouts": map[string]any{"header_ms": 200}},
			timeoutAccount(u, "acct-a", "sk-secret-a", nil),
			timeoutAccount(u, "acct-b", "sk-secret-b", nil),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading reply: %v", err)
		}
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "from-b") {
			t.Fatalf("status=%d body=%s, want 200 from second account", resp.StatusCode, body)
		}
		if got := usedKeys(u); !slices.Equal(got, []string{"sk-secret-a", "sk-secret-b"}) {
			t.Errorf("upstream keys = %v, want [sk-secret-a sk-secret-b]", got)
		}
		if !hasTimeoutLog(logs, "header", "acct-a", "200ms") {
			t.Errorf("expected timeout log line naming header, acct-a, and 200ms; got:\n%s", strings.Join(logs.lines, "\n"))
		}
	})

	t.Run("all accounts hang on headers", func(t *testing.T) {
		u := newUpstream(t)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		base, logs := startTimeoutCore(t, u,
			map[string]any{"timeouts": map[string]any{"header_ms": 150}},
			timeoutAccount(u, "acct-a", "sk-secret-a", nil),
			timeoutAccount(u, "acct-b", "sk-secret-b", nil),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("status=200, want failure when both accounts hang")
		}
		if got := usedKeys(u); !slices.Equal(got, []string{"sk-secret-a", "sk-secret-b"}) {
			t.Errorf("upstream keys = %v, want [sk-secret-a sk-secret-b]", got)
		}
		if !hasTimeoutLog(logs, "header", "acct-a", "150ms") || !hasTimeoutLog(logs, "header", "acct-b", "150ms") {
			t.Errorf("expected timeout logs for both acct-a and acct-b; got:\n%s", strings.Join(logs.lines, "\n"))
		}
	})
}

// 2. Headers then silence on a stream ends the stream with an error after the idle limit and does
// not rotate to another account.
func TestUpstreamTimeoutStreamIdle(t *testing.T) {
	t.Run("chunk then silence", func(t *testing.T) {
		u := newUpstream(t)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if keyOf(r) != "sk-secret-a" {
				okReply(w)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			const head = `"id":"chatcmpl-a","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test"`
			io.WriteString(w, `data: {`+head+`,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`+"\n\n")
			f.Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		base, logs := startTimeoutCore(t, u,
			map[string]any{"timeouts": map[string]any{"header_ms": 2000, "idle_ms": 250}},
			timeoutAccount(u, "acct-a", "sk-secret-a", nil),
			timeoutAccount(u, "acct-b", "sk-secret-b", nil),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("status=%d: %s", resp.StatusCode, b)
		}
		var data []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				data = append(data, d)
			}
		}
		if sc.Err() != nil {
			t.Fatalf("scanner error: %v", sc.Err())
		}
		hello := slices.IndexFunc(data, func(d string) bool { return strings.Contains(d, `"Hello"`) })
		failed := slices.IndexFunc(data, func(d string) bool { return strings.Contains(d, `"error"`) })
		if hello < 0 || failed < hello {
			t.Errorf("client stream = %q, want Hello then error", data)
		}
		if got := usedKeys(u); !slices.Equal(got, []string{"sk-secret-a"}) {
			t.Errorf("upstream keys = %v, want only [sk-secret-a]", got)
		}
		if !hasTimeoutLog(logs, "idle", "acct-a", "250ms") {
			t.Errorf("expected timeout log line naming idle, acct-a, and 250ms; got:\n%s", strings.Join(logs.lines, "\n"))
		}
	})

	t.Run("headers only then silence", func(t *testing.T) {
		u := newUpstream(t)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if keyOf(r) != "sk-secret-a" {
				okReply(w)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		base, logs := startTimeoutCore(t, u,
			map[string]any{"timeouts": map[string]any{"header_ms": 2000, "idle_ms": 200}},
			timeoutAccount(u, "acct-a", "sk-secret-a", nil),
			timeoutAccount(u, "acct-b", "sk-secret-b", nil),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("status=200, want error when headers sent then silence")
		}
		if got := usedKeys(u); !slices.Equal(got, []string{"sk-secret-a"}) {
			t.Errorf("upstream keys = %v, want only [sk-secret-a] (no rotation after headers)", got)
		}
		if !hasTimeoutLog(logs, "idle", "acct-a", "200ms") {
			t.Errorf("expected timeout log line naming idle, acct-a, and 200ms; got:\n%s", strings.Join(logs.lines, "\n"))
		}
	})
}

// 3. A slow reply that is not streamed ends after the total limit (and is not cut by header_ms).
func TestUpstreamTimeoutNonStreamTotal(t *testing.T) {
	u := newUpstream(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-secret-slow-ok" {
			// Slower than header_ms (50ms), faster than total_ms (300ms): must succeed.
			time.Sleep(120 * time.Millisecond)
			okReply(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
	base, logs := startTimeoutCore(t, u,
		map[string]any{"timeouts": map[string]any{"header_ms": 50, "total_ms": 300}},
		timeoutAccount(u, "acct-slow-ok", "sk-secret-slow-ok", nil),
		timeoutAccount(u, "acct-hang", "sk-secret-hang", nil),
	)

	// Non-streamed reply slower than header_ms succeeds within total_ms.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	req1, _ := http.NewRequestWithContext(ctx1, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(plainBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+clientKey)
	req1.Header.Set("X-Pluxy-Account", "acct-slow-ok")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("slow-ok request failed: %v", err)
	}
	raw1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("slow-ok status=%d body=%s, want 200", resp1.StatusCode, raw1)
	}

	// Non-streamed reply that hangs past total_ms fails around total_ms.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(plainBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+clientKey)
	req2.Header.Set("X-Pluxy-Account", "acct-hang")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("hang request failed: %v", err)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("hang request succeeded unexpectedly: %s", raw2)
	}
	if !hasTimeoutLog(logs, "total", "acct-hang", "300ms") {
		t.Errorf("expected timeout log line naming total, acct-hang, and 300ms; got:\n%s", strings.Join(logs.lines, "\n"))
	}
}

// 4. A long healthy stream whose gaps are shorter than idle_ms is not cut, even when total
// stream duration exceeds both idle_ms and total_ms several times.
func TestUpstreamTimeoutHealthyStreamNotCut(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		const head = `"id":"chatcmpl-long","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test"`
		for i := range 6 {
			if i > 0 {
				time.Sleep(150 * time.Millisecond)
			}
			fmt.Fprintf(w, `data: {`+head+`,"choices":[{"index":0,"delta":{"content":"p%d"},"finish_reason":null}]}`+"\n\n", i)
			f.Flush()
		}
		io.WriteString(w, `data: {`+head+`,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}
	base, _ := startTimeoutCore(t, u,
		map[string]any{"timeouts": map[string]any{"header_ms": 300, "idle_ms": 350, "total_ms": 200}},
		timeoutAccount(u, "acct-a", "sk-secret-a", nil),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "p5") || !strings.Contains(string(body), "[DONE]") || strings.Contains(string(body), `"error"`) {
		t.Fatalf("stream cut or failed: status=%d body=%s", resp.StatusCode, body)
	}
}

// 5. One account overriding a limit (raising it or turning it off with 0) is honoured.
func TestUpstreamTimeoutAccountOverride(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		time.Sleep(250 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		const head = `"id":"chatcmpl-ov","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test"`
		io.WriteString(w, `data: {`+head+`,"choices":[{"index":0,"delta":{"role":"assistant","content":"ok-ov"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}
	base, _ := startTimeoutCore(t, u,
		map[string]any{"timeouts": map[string]any{"header_ms": 100, "idle_ms": 100}},
		timeoutAccount(u, "acct-override", "sk-secret-override", map[string]any{"header_ms": 800}),
		timeoutAccount(u, "acct-off", "sk-secret-off", map[string]any{"header_ms": 0}),
		timeoutAccount(u, "acct-default", "sk-secret-default", nil),
	)
	send := func(acct string) (int, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("X-Pluxy-Account", acct)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s request failed: %v", acct, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if st, b := send("acct-override"); st != http.StatusOK || !strings.Contains(b, "ok-ov") {
		t.Errorf("acct-override: status=%d body=%s, want 200 ok-ov", st, b)
	}
	if st, b := send("acct-off"); st != http.StatusOK || !strings.Contains(b, "ok-ov") {
		t.Errorf("acct-off: status=%d body=%s, want 200 ok-ov (0 turns header limit off)", st, b)
	}
	if st, b := send("acct-default"); st == http.StatusOK {
		t.Errorf("acct-default: status=%d body=%s, want failure after 100ms header limit", st, b)
	}
}

// Codex's WebSocket idle timeout folds into routing.timeouts.idle_ms and logs idle, account,
// and the limit value when it fires.
func TestUpstreamTimeoutCodexWebSocketIdle(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_ws","object":"response","status":"in_progress","model":"gpt-test","output":[]}}`))
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)

	base, logs := runCoreLogged(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing":        map[string]any{"timeouts": map[string]any{"idle_ms": 200}},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-ws",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": srv.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-secret-ws"},
			}},
		},
	})
	c := dialResponses(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, _ := json.Marshal(wsCreate("hello", ""))
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	var gotError bool
	for range 5 {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("ws read: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		if m["type"] == "error" {
			gotError = true
			break
		}
	}
	if !gotError {
		t.Fatal("expected WebSocket error event after idle timeout")
	}
	if !hasTimeoutLog(logs, "idle", "acct-ws", "200ms") {
		t.Errorf("expected timeout log line naming idle, acct-ws, and 200ms; got:\n%s", strings.Join(logs.lines, "\n"))
	}
}
