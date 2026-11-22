package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRateLimitHeadersForwarding(t *testing.T) {
	// Fake Anthropic Messages upstream that attaches documented rate-limit headers plus sensitive headers.
	claudeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-requests-limit", "50")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "49")
		w.Header().Set("anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00Z")
		w.Header().Set("anthropic-ratelimit-input-tokens-limit", "40000")
		w.Header().Set("anthropic-ratelimit-input-tokens-remaining", "39000")
		w.Header().Set("anthropic-ratelimit-output-tokens-limit", "8000")
		w.Header().Set("anthropic-priority-input-tokens-limit", "100000")
		w.Header().Set("anthropic-fast-output-tokens-remaining", "50000")
		// Sensitive or non-rate-limit headers that must never be forwarded.
		w.Header().Set("Set-Cookie", "session=secret-cookie")
		w.Header().Set("Anthropic-Organization-Id", "org_secret_123")
		w.Header().Set("Anthropic-Workspace-Id", "wrk_secret_456")
		w.Header().Set("Authorization", "Bearer leaked-token")
		w.Header().Set("X-Api-Key", "sk-ant-leaked")
		w.Header().Set("X-Custom-Debug", "should-not-leak")

		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "trigger-429") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"too many requests"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer claudeUpstream.Close()

	// Fake OpenAI Chat Completions upstream with OpenAI + OpenRouter rate-limit headers plus sensitive headers.
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "60")
		w.Header().Set("x-ratelimit-remaining-requests", "59")
		w.Header().Set("x-ratelimit-reset-requests", "1s")
		w.Header().Set("x-ratelimit-limit-tokens", "150000")
		w.Header().Set("x-ratelimit-remaining-tokens", "149984")
		w.Header().Set("x-ratelimit-reset-tokens", "6m0s")
		w.Header().Set("x-ratelimit-limit-project-tokens", "60000")
		w.Header().Set("x-ratelimit-remaining-project-tokens", "57000")
		w.Header().Set("x-ratelimit-reset-project-tokens", "3s")
		// Sensitive or non-rate-limit headers that must never be forwarded.
		w.Header().Set("Set-Cookie", "cf_clearance=secret")
		w.Header().Set("Openai-Organization", "org-secret-789")
		w.Header().Set("Openai-Project", "proj_secret_abc")
		w.Header().Set("X-Upstream-Token", "tok-leaked")

		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "trigger-429") {
			w.Header().Set("X-RateLimit-Limit", "200")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1795248000000")
			w.Header().Set("Retry-After", "15")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"chat-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer chatUpstream.Close()

	// Fake OpenAI Responses upstream (served via openai-compatible wire_api: responses).
	responsesUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "100")
		w.Header().Set("x-ratelimit-remaining-requests", "98")
		w.Header().Set("x-ratelimit-reset-requests", "2s")
		w.Header().Set("Set-Cookie", "resp_cookie=secret")
		w.Header().Set("Openai-Organization", "org-resp-secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"resp-test","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer responsesUpstream.Close()

	claudeBase := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "claude-acc",
					"models":     []string{"claude-test", "claude-429-same", "claude-429-cross"},
					"settings":   map[string]any{"base_url": claudeUpstream.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-ant-test-key"},
				},
			},
		},
	})
	oaiBase := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "chat-acc",
					"models":     []string{"chat-test", "chat-429-same"},
					"settings":   map[string]any{"base_url": chatUpstream.URL, "wire_api": "chat"},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-chat-test-key"},
				},
				map[string]any{
					"id":         "resp-acc",
					"models":     []string{"resp-test"},
					"settings":   map[string]any{"base_url": responsesUpstream.URL, "wire_api": "responses"},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-resp-test-key"},
				},
			},
		},
	})

	codexBase := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "codex-acc",
					"models":     []string{"codex-test"},
					"settings":   map[string]any{"base_url": responsesUpstream.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-codex-test-key"},
				},
			},
		},
	})

	doPost := func(t *testing.T, base, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+clientKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}

	assertNoSensitive := func(t *testing.T, h http.Header) {
		t.Helper()
		for _, forbidden := range []string{
			"Set-Cookie", "Anthropic-Organization-Id", "Anthropic-Workspace-Id",
			"Openai-Organization", "Openai-Project", "Authorization",
			"X-Api-Key", "X-Custom-Debug", "X-Upstream-Token",
		} {
			if got := h.Get(forbidden); got != "" {
				t.Errorf("forbidden header %s leaked: %q", forbidden, got)
			}
		}
	}

	t.Run("same-format claude non-stream and stream", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			body := `{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			if stream {
				body = `{"model":"claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			resp := doPost(t, claudeBase, "/v1/messages", body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("stream=%v status=%d", stream, resp.StatusCode)
			}
			for k, want := range map[string]string{
				"anthropic-ratelimit-requests-limit":     "50",
				"anthropic-ratelimit-requests-remaining": "49",
				"anthropic-ratelimit-requests-reset":     "2026-11-22T08:00:00Z",
				"anthropic-ratelimit-input-tokens-limit": "40000",
				"anthropic-priority-input-tokens-limit":  "100000",
				"anthropic-fast-output-tokens-remaining": "50000",
			} {
				if got := resp.Header.Get(k); got != want {
					t.Errorf("stream=%v header %s = %q, want %q", stream, k, got, want)
				}
			}
			assertNoSensitive(t, resp.Header)
		}
	})

	t.Run("same-format openai chat non-stream and stream", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			body := `{"model":"chat-test","messages":[{"role":"user","content":"hi"}]}`
			if stream {
				body = `{"model":"chat-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			resp := doPost(t, oaiBase, "/v1/chat/completions", body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("stream=%v status=%d", stream, resp.StatusCode)
			}
			for k, want := range map[string]string{
				"x-ratelimit-limit-requests":           "60",
				"x-ratelimit-remaining-requests":       "59",
				"x-ratelimit-reset-requests":           "1s",
				"x-ratelimit-limit-tokens":             "150000",
				"x-ratelimit-remaining-tokens":         "149984",
				"x-ratelimit-reset-tokens":             "6m0s",
				"x-ratelimit-limit-project-tokens":     "60000",
				"x-ratelimit-remaining-project-tokens": "57000",
				"x-ratelimit-reset-project-tokens":     "3s",
			} {
				if got := resp.Header.Get(k); got != want {
					t.Errorf("stream=%v header %s = %q, want %q", stream, k, got, want)
				}
			}
			assertNoSensitive(t, resp.Header)
		}
	})

	t.Run("same-format openai responses and codex", func(t *testing.T) {
		resp := doPost(t, oaiBase, "/v1/responses", `{"model":"resp-test","input":"hi"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("oai responses status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("x-ratelimit-limit-requests"); got != "100" {
			t.Errorf("oai responses x-ratelimit-limit-requests = %q, want 100", got)
		}
		assertNoSensitive(t, resp.Header)

		resp = doPost(t, codexBase, "/v1/responses", `{"model":"codex-test","input":"hi"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("codex responses status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("x-ratelimit-limit-requests"); got != "100" {
			t.Errorf("codex responses x-ratelimit-limit-requests = %q, want 100", got)
		}
		assertNoSensitive(t, resp.Header)
	})

	t.Run("cross-format drops foreign rate-limit headers", func(t *testing.T) {
		// Claude upstream -> OpenAI Chat client: no anthropic-ratelimit-* headers.
		resp := doPost(t, claudeBase, "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("claude->chat status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("anthropic-ratelimit-requests-limit"); got != "" {
			t.Errorf("claude->chat leaked anthropic-ratelimit-requests-limit = %q", got)
		}
		assertNoSensitive(t, resp.Header)

		// OpenAI Chat upstream -> Anthropic Messages client: no x-ratelimit-* headers.
		resp = doPost(t, oaiBase, "/v1/messages", `{"model":"chat-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chat->messages status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("x-ratelimit-limit-requests"); got != "" {
			t.Errorf("chat->messages leaked x-ratelimit-limit-requests = %q", got)
		}
		assertNoSensitive(t, resp.Header)

		// OpenAI Chat upstream -> Gemini client: Gemini documents no rate-limit headers.
		resp = doPost(t, oaiBase, "/v1beta/models/chat-test:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chat->gemini status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("x-ratelimit-limit-requests"); got != "" {
			t.Errorf("chat->gemini leaked x-ratelimit-limit-requests = %q", got)
		}
		assertNoSensitive(t, resp.Header)
	})

	t.Run("429 error replies forward rate-limit headers on same format only", func(t *testing.T) {
		// OpenRouter-style 429 on Chat -> Chat client gets X-RateLimit-Limit/Remaining/Reset.
		resp := doPost(t, oaiBase, "/v1/chat/completions", `{"model":"chat-429-same","messages":[{"role":"user","content":"trigger-429"}]}`)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("chat 429 status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("X-RateLimit-Limit"); got != "200" {
			t.Errorf("X-RateLimit-Limit = %q, want 200", got)
		}
		if got := resp.Header.Get("X-RateLimit-Remaining"); got != "0" {
			t.Errorf("X-RateLimit-Remaining = %q, want 0", got)
		}
		if got := resp.Header.Get("X-RateLimit-Reset"); got != "1795248000000" {
			t.Errorf("X-RateLimit-Reset = %q, want 1795248000000", got)
		}
		assertNoSensitive(t, resp.Header)

		// Claude 429 -> Messages client gets anthropic-ratelimit-requests-limit.
		resp = doPost(t, claudeBase, "/v1/messages", `{"model":"claude-429-same","max_tokens":16,"messages":[{"role":"user","content":"trigger-429"}]}`)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("claude 429 status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("anthropic-ratelimit-requests-limit"); got != "50" {
			t.Errorf("claude 429 anthropic-ratelimit-requests-limit = %q, want 50", got)
		}
		assertNoSensitive(t, resp.Header)

		// Claude 429 -> Chat client must NOT get anthropic-ratelimit-requests-limit.
		resp = doPost(t, claudeBase, "/v1/chat/completions", `{"model":"claude-429-cross","messages":[{"role":"user","content":"trigger-429"}]}`)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("claude->chat 429 status=%d", resp.StatusCode)
		}
		if got := resp.Header.Get("anthropic-ratelimit-requests-limit"); got != "" {
			t.Errorf("claude->chat 429 leaked anthropic-ratelimit-requests-limit = %q", got)
		}
		assertNoSensitive(t, resp.Header)
	})
}
