package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An upstream's error that echoes a piece of the account's API key (its first 24 characters, then
// "...", as many upstreams write a key they refuse) reaches neither the client, core's log, the
// account's last error nor the request log, for any extension: core removes the pieces itself.
// The key is fake.

const pieceKey = "sk-a40-Qx7Rv2Lm9Tz4Wp8Ys3Nk6Jd1Hf5Gc0BqX"

// pieceTarget is one extension, the client request that reaches it and the upstream's refusal.
type pieceTarget struct {
	name     string
	dir      string // the extension's folder under binDir ("" for openai-compatible)
	model    string
	path     string // the client's endpoint
	body     string
	logPref  string // the request log's name prefix for path
	marker   string // the upstream's message before the echoed key
	settings func(upURL string) map[string]any
	auth     func(r *http.Request)
	refusal  func(echo string) string // the upstream's 401 body with echo in its message
}

var pieceTargets = []pieceTarget{{
	name:     "openai-compatible",
	model:    "gpt-a40",
	path:     "/v1/chat/completions",
	body:     `{"model":"gpt-a40","messages":[{"role":"user","content":"hi"}]}`,
	logPref:  "v1-chat-completions-",
	marker:   "Incorrect API key provided",
	settings: func(u string) map[string]any { return map[string]any{"base_url": u + "/v1"} },
	auth:     func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+clientKey) },
	refusal: func(echo string) string {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": "Incorrect API key provided: " + echo + " You can find your API key in the dashboard.",
			"type":    "invalid_request_error", "param": nil, "code": "invalid_api_key",
		}})
		return string(b)
	},
}, {
	name:     "claude",
	dir:      "claude",
	model:    "claude-a40",
	path:     "/v1/messages",
	body:     `{"model":"claude-a40","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
	logPref:  "v1-messages-",
	marker:   "invalid x-api-key",
	settings: func(u string) map[string]any { return map[string]any{"base_url": u} },
	auth: func(r *http.Request) {
		r.Header.Set("X-Api-Key", clientKey)
		r.Header.Set("Anthropic-Version", "2023-06-01")
	},
	refusal: func(echo string) string {
		b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{
			"type": "authentication_error", "message": "invalid x-api-key: " + echo,
		}})
		return string(b)
	},
}}

// runPiece sends tg's request through core to tg's extension, whose upstream answers status with
// body, and returns what that showed. It waits for core to log a line holding wait.
func runPiece(t *testing.T, tg pieceTarget, status int, body, wait string) leakRun {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	account := map[string]any{
		"id":         "piece-1",
		"models":     []string{tg.model},
		"settings":   tg.settings(up.URL),
		"credential": map[string]any{"kind": "api_key", "api_key": pieceKey},
	}
	base, path, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"logs":           map[string]any{"request_log": true},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, tg.dir, "pluxy-extension.json"),
			"accounts": []any{account},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+tg.path, strings.NewReader(tg.body))
	req.Header.Set("Content-Type", "application/json")
	tg.auth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r := leakRun{status: resp.StatusCode, body: string(b), logs: logs}
	if status >= 400 {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if a := getAccount(t, base, "piece-1"); a.LastError != nil {
				r.lastError = a.LastError.Code + ": " + a.LastError.Message
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the account has no last error")
			}
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "logs", tg.logPref+"*.log"))
		if len(matches) == 1 {
			raw, _ := os.ReadFile(matches[0])
			if r.reqLog = string(raw); strings.Contains(r.reqLog, "=== RESPONSE ===") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request logs: %v", matches)
		}
	}
	if !strings.Contains(r.reqLog, "=== UPSTREAM") {
		t.Fatalf("the request log has no upstream capture:\n%s", r.reqLog)
	}
	for deadline := time.Now().Add(5 * time.Second); wait != "" && !logs.contains(wait); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("core logged no line holding %q", wait)
		}
	}
	return r
}

func TestUpstreamErrorKeyPiece(t *testing.T) {
	piece := pieceKey[:24]
	for _, tg := range pieceTargets {
		t.Run(tg.name+"/piece", func(t *testing.T) {
			r := runPiece(t, tg, http.StatusUnauthorized, tg.refusal(piece+"..."), tg.marker)
			if r.status != http.StatusUnauthorized {
				t.Errorf("status %d, want 401: %s", r.status, r.body)
			}
			r.wantNoSecret(t, piece, piece[8:16])
			// The rest of the message is kept in each place.
			for _, place := range []struct{ name, text string }{{"client", r.body}, {"last error", r.lastError}, {"request log", r.reqLog}} {
				if !strings.Contains(place.text, tg.marker) {
					t.Errorf("the %s lost the upstream's message %q: %s", place.name, tg.marker, place.text)
				}
			}
		})
		t.Run(tg.name+"/whole", func(t *testing.T) {
			r := runPiece(t, tg, http.StatusUnauthorized, tg.refusal(pieceKey+"."), tg.marker)
			if r.status != http.StatusUnauthorized {
				t.Errorf("status %d, want 401: %s", r.status, r.body)
			}
			r.wantNoSecret(t, pieceKey, piece)
		})
	}
}

// A successful reply is the model's output: words in it that share characters with the account's
// key come through as the upstream sent them, to the client and in the request log.
func TestSuccessfulReplyKeepsKeyLikeWords(t *testing.T) {
	const content = "Qx7Rv2Lm and Hf5Gc0Bq are fine"
	reply, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-a40", "object": "chat.completion", "created": 1700000000, "model": "gpt-a40",
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": content}}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
	})
	r := runPiece(t, pieceTargets[0], http.StatusOK, string(reply), "")
	if r.status != http.StatusOK || !strings.Contains(r.body, content) {
		t.Errorf("client got %d %s, want %q", r.status, r.body, content)
	}
	if strings.Count(r.reqLog, content) < 2 {
		t.Errorf("the request log does not hold %q in both the upstream capture and the reply:\n%s", content, r.reqLog)
	}
}
