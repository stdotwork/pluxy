package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// An upstream's "the prompt is too long" reaches each client in the shape it acts on: code
// context_length_exceeded with status 400 for Chat and Responses (what the Codex CLI compacts on),
// a 400 invalid_request_error whose message says "prompt is too long" for Messages, and a 400
// INVALID_ARGUMENT for Gemini. It fails that request only: the same account serves the next one.
func TestContextOverflowReachesEachClient(t *testing.T) {
	upstreams := []struct {
		name, dir, model, reply string
	}{
		{"gemini", "gemini", "gemini-a40",
			`{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`},
		{"openai-compatible", "", "gpt-a40",
			`{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error","param":"messages","code":null}}`},
		{"claude", "claude", "claude-a40",
			`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"},"request_id":"req_a40"}`},
	}
	for _, u := range upstreams {
		t.Run(u.name, func(t *testing.T) {
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The account check core runs at start is not one of the client's requests.
				if b, _ := io.ReadAll(r.Body); strings.Contains(string(b), overflowPrompt) {
					calls.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, u.reply)
			}))
			t.Cleanup(up.Close)
			base, _, _ := runCoreArgs(t, map[string]any{
				"listen":         "127.0.0.1:0",
				"client_keys":    []string{clientKey},
				"management_key": managementKey,
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, u.dir, "pluxy-extension.json"),
					"accounts": []any{map[string]any{
						"id": "overflow-1", "models": []string{u.model},
						"settings":   map[string]any{"base_url": up.URL},
						"credential": map[string]any{"kind": "api_key", "api_key": "sk-overflow-test"},
					}},
				},
			})
			clients := []struct {
				name, path, body string
				check            func(status int, body []byte) string
			}{
				{"chat", "/v1/chat/completions", `{"model":"` + u.model + `","messages":[{"role":"user","content":"` + overflowPrompt + `"}]}`, wantOverflowCode},
				{"responses", "/v1/responses", `{"model":"` + u.model + `","input":"` + overflowPrompt + `"}`, wantOverflowCode},
				{"messages", "/v1/messages", `{"model":"` + u.model + `","max_tokens":8,"messages":[{"role":"user","content":"` + overflowPrompt + `"}]}`, wantOverflowMessages},
				{"gemini", "/v1beta/models/" + u.model + ":generateContent", `{"contents":[{"role":"user","parts":[{"text":"` + overflowPrompt + `"}]}]}`, wantOverflowGemini},
			}
			for i, c := range clients {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+c.path, strings.NewReader(c.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+clientKey)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				cancel()
				if msg := c.check(resp.StatusCode, b); msg != "" {
					t.Errorf("%s client: %s: %d %s", c.name, msg, resp.StatusCode, b)
				}
				// Each request reached the upstream once: the one before blocked nothing and was
				// not tried again.
				if got := calls.Load(); got != int64(i+1) {
					t.Errorf("after the %s request the upstream saw %d requests, want %d", c.name, got, i+1)
				}
			}
			// A Responses WebSocket client gets the same code in its error message.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": {"Bearer " + clientKey}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			create := `{"type":"response.create","model":"` + u.model + `","input":[{"role":"user","content":"` + overflowPrompt + `"}]}`
			if err := conn.Write(ctx, websocket.MessageText, []byte(create)); err != nil {
				t.Fatal(err)
			}
			_, frame, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Status int `json:"status"`
			}
			_ = json.Unmarshal(frame, &m)
			if m.Status != http.StatusBadRequest || overflowError(frame)["code"] != "context_length_exceeded" {
				t.Errorf("WebSocket message = %s, want status 400 and code context_length_exceeded", frame)
			}
		})
	}
}

const overflowPrompt = "overflow-test-prompt"

func overflowError(body []byte) map[string]any {
	var b struct {
		Error map[string]any `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Error
}

func wantOverflowCode(status int, body []byte) string {
	if status != http.StatusBadRequest || overflowError(body)["code"] != "context_length_exceeded" {
		return "want status 400 and code context_length_exceeded"
	}
	return ""
}

func wantOverflowMessages(status int, body []byte) string {
	e := overflowError(body)
	msg, _ := e["message"].(string)
	if status != http.StatusBadRequest || e["type"] != "invalid_request_error" || !strings.Contains(msg, "prompt is too long") {
		return `want status 400, invalid_request_error and "prompt is too long"`
	}
	return ""
}

func wantOverflowGemini(status int, body []byte) string {
	e := overflowError(body)
	if status != http.StatusBadRequest || e["status"] != "INVALID_ARGUMENT" || e["code"] != float64(400) {
		return "want status 400 and INVALID_ARGUMENT"
	}
	return ""
}

// An overflow an upstream reports inside a 200 stream is the same refusal: it does not count
// against the account, so the next request still reaches the upstream.
func TestContextOverflowInStreamBlocksNothing(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); strings.Contains(string(b), overflowPrompt) {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"error":{"message":"This model's maximum context length is 8192 tokens.","code":null}}`+"\n\n")
	}))
	t.Cleanup(up.Close)
	base, _, _ := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "overflow-1", "models": []string{"gpt-a40"},
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-overflow-test"},
			}},
		},
	})
	body := `{"model":"gpt-a40","stream":true,"messages":[{"role":"user","content":"` + overflowPrompt + `"}]}`
	for i := 1; i <= 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if !strings.Contains(string(b), "context_length_exceeded") {
			t.Errorf("request %d: %d %s, want code context_length_exceeded", i, resp.StatusCode, b)
		}
		if got := calls.Load(); got != int64(i) {
			t.Errorf("after request %d the upstream saw %d requests", i, got)
		}
	}
}
