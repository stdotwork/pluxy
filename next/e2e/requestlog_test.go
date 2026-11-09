package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Request logs: every request with request-log on, failed ones as error logs with it off, with
// every secret left out, read back with the management calls.

type errorLogList struct {
	Files []struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified int64  `json:"modified"`
	} `json:"files"`
}

func (c *mcore) errorLogs(t *testing.T) errorLogList {
	t.Helper()
	var l errorLogList
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/request-error-logs", nil), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// download fetches a log file and checks it is sent as an attachment.
func (c *mcore) download(t *testing.T, path string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+"/v0/management"+path, nil)
	req.Header.Set("Authorization", "Bearer "+managementKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("GET %s: %d %q %s", path, resp.StatusCode, resp.Header.Get("Content-Disposition"), b)
	}
	return string(b)
}

// requestWithSecrets sends a chat request that carries secrets where a log could pick them up:
// the client key in its header and its body, another client key and the upstream key in
// headers and the body.
func requestWithSecrets(t *testing.T, base, pin string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":"my keys are %s and %s"}]}`, secondClientKey, upstreamKey)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions?key="+secondClientKey, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("X-Goog-Api-Key", secondClientKey)
	req.Header.Set("Cookie", "session="+upstreamKey)
	if pin != "" {
		req.Header.Set("X-Pluxy-Account", pin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("X-CPA-TRACE-ID")
}

func TestRequestLogs(t *testing.T) {
	c := startManaged(t, false)
	dir := c.logsDir()
	secrets := []string{clientKey, secondClientKey, upstreamKey, managementKey}

	// With request-log off (the default), a request that succeeds leaves no file; one that
	// fails leaves an error log.
	if code, _ := requestWithSecrets(t, c.base, ""); code != http.StatusOK {
		t.Fatalf("plain request: %d", code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("a successful request with request-log off wrote %v", entries)
	}
	if code, _ := requestWithSecrets(t, c.base, "nobody"); code != http.StatusNotFound {
		t.Fatalf("pinned to no account: %d", code)
	}
	l := c.errorLogs(t)
	if len(l.Files) != 1 || !strings.HasPrefix(l.Files[0].Name, "error-v1-chat-completions-") || l.Files[0].Size == 0 ||
		time.Since(time.Unix(l.Files[0].Modified, 0)) > time.Minute {
		t.Fatalf("error logs: %+v", l)
	}
	text := c.download(t, "/request-error-logs/"+l.Files[0].Name)
	wantNoSecrets(t, "the error log", text, secrets...)
	for _, want := range []string{"=== REQUEST INFO ===", "URL: /v1/chat/completions?key=%5Bredacted%5D", "Method: POST",
		"=== HEADERS ===", "Authorization: Bearer [redacted]", "X-Goog-Api-Key: [redacted]", "Cookie: [redacted]",
		"=== REQUEST BODY ===", `"my keys are [redacted] and [redacted]"`, "=== RESPONSE ===", "Status: 404", `does not exist or does not serve model`} {
		if !strings.Contains(text, want) {
			t.Errorf("the error log lacks %q:\n%s", want, text)
		}
	}
	// The file on disk is what the call sent.
	if raw, err := os.ReadFile(filepath.Join(dir, l.Files[0].Name)); err != nil || string(raw) != text {
		t.Fatalf("file on disk: %v", err)
	}

	// With request-log on, every request is written; its trace ID finds it.
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"request-log": true}}}`)
	code, trace := requestWithSecrets(t, c.base, "")
	if code != http.StatusOK || trace == "" {
		t.Fatalf("logged request: %d %q", code, trace)
	}
	text = c.download(t, "/request-log-by-id/"+trace)
	wantNoSecrets(t, "the request log", text, secrets...)
	for _, want := range []string{"Trace ID: " + trace, "Account: upstream-1", "Status: 200", "Content-Type: application/json", `"content":"Hello, world!"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the request log lacks %q:\n%s", want, text)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "v1-chat-completions-*-"+trace+".log")); len(matches) != 1 {
		t.Fatalf("request log files for %s: %v", trace, matches)
	}
	if code, b := c.call(t, http.MethodGet, "/request-log-by-id/no-such-trace", nil); code != http.StatusNotFound {
		t.Fatalf("unknown trace: %d %s", code, b)
	}
	for _, bad := range []string{"/request-error-logs/main.log", "/request-error-logs/error-..%2F..%2Fconfig.json.log", "/request-error-logs/error-x.txt"} {
		if code, b := c.call(t, http.MethodGet, bad, nil); code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", bad, code, b)
		}
	}
	// A request log is not an error log, so it is not listed.
	if l := c.errorLogs(t); len(l.Files) != 1 {
		t.Fatalf("error logs after a request log: %+v", l)
	}

	// error-logs-max-files keeps the newest.
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"request-log": false, "error-logs-max-files": 2}}}`)
	for i := range 3 {
		time.Sleep(10 * time.Millisecond) // distinct modification times
		requestWithSecrets(t, c.base, fmt.Sprint("nobody-", i))
	}
	l = c.errorLogs(t)
	if len(l.Files) != 2 {
		t.Fatalf("with error-logs-max-files 2: %+v", l)
	}
	newest := c.download(t, "/request-error-logs/"+l.Files[0].Name)
	if !strings.Contains(newest, "X-Pluxy-Account: nobody-2") {
		t.Fatalf("newest error log is not the last request's:\n%s", newest)
	}
	if code, b := c.call(t, http.MethodPatch, "/settings", `{"observability": {"logs": {"error-logs-max-files": -1}}}`); code != http.StatusBadRequest {
		t.Fatalf("negative count: %d %s", code, b)
	}
}

// A streamed reply that fails after it began is a failure: its error log holds the reply up to
// the failure.
func TestErrorLogForStreamFailingMidway(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// The stream ends with no finish and no [DONE].
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "a", "settings": map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
		},
	})
	os.WriteFile(cfgPath, b, 0o600)
	base, _ := runCoreFile(t, cfgPath, noWatch...)
	c := &mcore{base: base, path: cfgPath}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	l := c.errorLogs(t)
	if len(l.Files) != 1 {
		t.Fatalf("error logs: %+v", l)
	}
	text := c.download(t, "/request-error-logs/"+l.Files[0].Name)
	if !strings.Contains(text, "Status: 200") || !strings.Contains(text, "partial") || !strings.Contains(text, "error") {
		t.Fatalf("error log:\n%s", text)
	}
	wantNoSecrets(t, "the error log", text, clientKey, upstreamKey, managementKey)
}

// A Responses WebSocket turn is logged with the client's message and the messages sent back.
func TestRequestLogWebSocketTurn(t *testing.T) {
	u := newWSUpstream(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"logs":           map[string]any{"request_log": true},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "acct-ws", "models": []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws-secret"}}},
		},
	})
	os.WriteFile(cfgPath, b, 0o600)
	base, _ := runCoreFile(t, cfgPath, noWatch...)
	conn := dialResponses(t, base)
	if m := wsTurn(t, conn, wsCreate("hello there", "")); m["type"] != "response.completed" {
		t.Fatalf("turn: %v", m)
	}
	var text string
	deadline := time.Now().Add(10 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(dir, "logs", "v1-responses-*.log"))
		if len(matches) == 1 {
			raw, _ := os.ReadFile(matches[0])
			text = string(raw)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn logs: %v", matches)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{"Method: GET", "Account: acct-ws", "Authorization: Bearer [redacted]", "hello there",
		"=== WEBSOCKET MESSAGES ===", "response.completed"} {
		if !strings.Contains(text, want) {
			t.Errorf("turn log lacks %q:\n%s", want, text)
		}
	}
	wantNoSecrets(t, "the turn log", text, clientKey, "sk-ws-secret", managementKey)
}
