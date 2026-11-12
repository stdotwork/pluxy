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
		"Client IP: 127.0.0.1",
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

	// A link planted in the folder is neither listed nor served, under an error log's name or
	// a trace ID's.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("not a log"), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "error-v1-x-2026-11-09T120000-20261109120000-00000000000000aa.log")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink(secret, filepath.Join(dir, "v1-x-2026-11-09T120000-20261109120000-0123456789abcdef.log"))
	for _, path := range []string{"/request-error-logs/error-v1-x-2026-11-09T120000-20261109120000-00000000000000aa.log", "/request-log-by-id/20261109120000-0123456789abcdef"} {
		if code, b := c.call(t, http.MethodGet, path, nil); code != http.StatusNotFound || strings.Contains(string(b), "not a log") {
			t.Fatalf("%s: %d %s", path, code, b)
		}
	}
	if l := c.errorLogs(t); len(l.Files) != 2 {
		t.Fatalf("listed with a planted link: %+v", l)
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
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"trusted_proxies": []string{"127.0.0.1"},
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("X-Forwarded-For", "198.51.100.7") // from a trusted proxy: the client
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(text, "Status: 200") || !strings.Contains(text, "partial") || !strings.Contains(text, "error") ||
		!strings.Contains(text, "Client IP: 198.51.100.7\n") {
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
	for _, want := range []string{"Method: GET", "Client IP: 127.0.0.1", "Account: acct-ws", "Authorization: Bearer [redacted]", "hello there",
		"=== WEBSOCKET MESSAGES ===", "response.completed"} {
		if !strings.Contains(text, want) {
			t.Errorf("turn log lacks %q:\n%s", want, text)
		}
	}
	// The turn opened the upstream socket: its handshake is logged, its messages are not.
	reqs, resps := sections(text, "UPSTREAM REQUEST"), sections(text, "UPSTREAM RESPONSE")
	if len(reqs) != 1 || len(resps) != 1 {
		t.Fatalf("%d upstream requests and %d responses in the turn log, want 1 and 1:\n%s", len(reqs), len(resps), text)
	}
	for _, want := range []string{"Account: acct-ws", "URL: " + u.URL + "/v1/responses", "Method: GET", "Authorization: Bearer [redacted]", "Upgrade: websocket"} {
		if !strings.Contains(reqs[0], want) {
			t.Errorf("the handshake request lacks %q:\n%s", want, reqs[0])
		}
	}
	if !strings.Contains(resps[0], "Status: 101") || !strings.Contains(resps[0], "its messages are not logged") || strings.Contains(resps[0], "response.completed") {
		t.Errorf("the handshake response:\n%s", resps[0])
	}
	wantNoSecrets(t, "the turn log", text, clientKey, "sk-ws-secret", managementKey)
}

// Both JSON-only UI hosts can read the same content as a plain download, with a bounded read.
func TestRequestLogJSON(t *testing.T) {
	c := startManaged(t, false)
	code, trace := requestWithSecrets(t, c.base, "nobody")
	if code != http.StatusNotFound {
		t.Fatalf("failed request: %d %q", code, trace)
	}
	files := c.errorLogs(t).Files
	if len(files) != 1 {
		t.Fatalf("error logs: %+v", files)
	}
	name := files[0].Name
	trace = strings.TrimSuffix(name[len(name)-35:], ".log")
	plain := c.download(t, "/request-error-logs/"+name)
	for _, path := range []string{"/request-error-logs/" + name, "/request-log-by-id/" + trace} {
		var got struct {
			Name    string
			Size    int64
			Content string
		}
		body := c.expect(t, http.StatusOK, http.MethodGet, path+"?format=json", nil)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GET %s?format=json is not JSON: %v; body: %.100s", path, err, body)
		}
		if got.Name != name || got.Size != int64(len(plain)) || got.Content != plain {
			t.Fatalf("GET %s: %+v", path, got)
		}
	}
	// Exactly 2 MiB is accepted. One byte more is refused without a partial reply.
	path := filepath.Join(c.logsDir(), name)
	big := strings.Repeat("x", 2<<20)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	var exact struct {
		Size    int64
		Content string
	}
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/request-error-logs/"+name+"?format=json", nil), &exact); err != nil {
		t.Fatal(err)
	}
	if exact.Size != 2<<20 || exact.Content != big {
		t.Fatal("2 MiB content changed")
	}
	if err := os.WriteFile(path, []byte(big+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/request-error-logs/" + name, "/request-log-by-id/" + trace} {
		body := c.expect(t, http.StatusRequestEntityTooLarge, http.MethodGet, route+"?format=json", nil)
		var failure struct {
			Error   struct{ Code string }
			Content *string
		}
		if err := json.Unmarshal(body, &failure); err != nil || failure.Error.Code != "log_too_large" || failure.Content != nil {
			t.Fatalf("oversize response: %.100s", body)
		}
	}
	if got := c.download(t, "/request-error-logs/"+name); got != big+"x" {
		t.Fatal("plain download was truncated")
	}
	// JSON keeps the download's protection against links out of the log folder.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(c.path, path); err != nil {
		t.Fatal(err)
	}
	c.expect(t, http.StatusNotFound, http.MethodGet, "/request-error-logs/"+name+"?format=json", nil)
}

// A request log leaves out the secrets in a config account's settings wherever it would show
// them, as it leaves out the account's credential: here in the client's message and in the reply,
// whose text the test extension makes from the account's settings.
func TestRequestLogHoldsNoSettingsSecret(t *testing.T) {
	const key, token, credential = "sk-log-settings-key", "sk-log-settings-token", "sk-log-credential"
	pkg := testPackage(t, "0.1.0")
	base, path, _ := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"logs":           map[string]any{"request_log": true},
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "set",
				"settings":   map[string]any{"api_key": key, "base_url": "https://up.example/v1?token=" + token},
				"credential": map[string]any{"kind": "api_key", "api_key": credential}}},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"account-settings","messages":[{"role":"user","content":"my setting is `+key+`"}]}`)
	reply, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(reply), token) {
		t.Fatalf("reply %d %s; want the account's settings as its text", resp.StatusCode, reply)
	}
	var text string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "logs", "v1-chat-completions-*.log"))
		if len(matches) == 1 {
			raw, _ := os.ReadFile(matches[0])
			if text = string(raw); strings.Contains(text, "=== RESPONSE ===") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request logs: %v", matches)
		}
	}
	wantNoSecrets(t, "the request log", text, key, token, credential, clientKey)
	if !strings.Contains(text, "my setting is [redacted]") || !strings.Contains(text, "Status: 200") {
		t.Errorf("the request log lacks the redacted message or the reply:\n%s", text)
	}
}
