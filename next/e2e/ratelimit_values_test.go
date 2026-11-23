package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestRateLimitHeaderValues checks that a client sees the numbers, times and durations an
// upstream sends in its rate-limit headers, and no other text: an ID, or a piece of the account's
// key, under an allowed header name is dropped, on a 200 and on a 429, through each extension.
func TestRateLimitHeaderValues(t *testing.T) {
	const (
		accountKey = "sk-sweepSecretKey0123456789abcdefXYZ"
		keyPiece   = "sk-sweepSecret" // its first 14 characters
		orgID      = "org_sweep_ID_123"
	)
	reply := func(w http.ResponseWriter, r *http.Request, set func(h http.Header), ok string) {
		set(w.Header())
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "trigger-429") {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
			return
		}
		_, _ = io.WriteString(w, ok)
	}

	claudeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, func(h http.Header) {
			h.Set("anthropic-ratelimit-requests-limit", orgID)
			h.Set("anthropic-ratelimit-tokens-limit", keyPiece)
			h.Set("anthropic-ratelimit-requests-remaining", "49")
			h.Set("anthropic-ratelimit-requests-reset", "2026-11-22T08:00:00Z")
			h.Set("anthropic-ratelimit-tokens-reset", "2026-11-22T18:00:00.5+10:00")
		}, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer claudeUpstream.Close()

	openaiHeaders := func(h http.Header) {
		h.Set("x-ratelimit-limit-requests", orgID)
		h.Set("x-ratelimit-limit-tokens", keyPiece)
		h.Set("x-ratelimit-remaining-requests", "59")
		h.Set("x-ratelimit-reset-requests", "1h2m3.5s")
		h.Set("x-ratelimit-reset-tokens", "6m0s")
		h.Set("x-ratelimit-reset-project-tokens", "17ms")
		h.Set("x-ratelimit-reset", "1795248000000")
	}
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, openaiHeaders, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"chat-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer chatUpstream.Close()
	responsesUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, openaiHeaders, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"codex-test","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer responsesUpstream.Close()

	core := func(manifest, model, baseURL string, settings map[string]any) string {
		settings["base_url"] = baseURL
		return runCore(t, map[string]any{
			"listen":          "127.0.0.1:0",
			"client_keys":     []string{clientKey},
			"management_key":  managementKey,
			"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
			"extension": map[string]any{
				"manifest": manifest,
				"accounts": []any{map[string]any{
					"id": "acc", "models": []string{model}, "settings": settings,
					"credential": map[string]any{"kind": "api_key", "api_key": accountKey},
				}},
			},
		})
	}
	claudeBase := core(filepath.Join(binDir, "claude", "pluxy-extension.json"), "claude-test", claudeUpstream.URL, map[string]any{})
	chatBase := core(filepath.Join(binDir, "pluxy-extension.json"), "chat-test", chatUpstream.URL, map[string]any{"wire_api": "chat"})
	codexBase := core(filepath.Join(binDir, "codex", "pluxy-extension.json"), "codex-test", responsesUpstream.URL, map[string]any{})

	openaiWant := map[string]string{
		"x-ratelimit-remaining-requests":   "59",
		"x-ratelimit-reset-requests":       "1h2m3.5s",
		"x-ratelimit-reset-tokens":         "6m0s",
		"x-ratelimit-reset-project-tokens": "17ms",
		"x-ratelimit-reset":                "1795248000000",
	}
	openaiDropped := []string{"x-ratelimit-limit-requests", "x-ratelimit-limit-tokens"}
	for _, tc := range []struct {
		name, base, path, model, body string
		want                          map[string]string
		dropped                       []string
	}{
		{"claude", claudeBase, "/v1/messages", "claude-test", `{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"%s"}]}`,
			map[string]string{
				"anthropic-ratelimit-requests-remaining": "49",
				"anthropic-ratelimit-requests-reset":     "2026-11-22T08:00:00Z",
				"anthropic-ratelimit-tokens-reset":       "2026-11-22T18:00:00.5+10:00",
			},
			[]string{"anthropic-ratelimit-requests-limit", "anthropic-ratelimit-tokens-limit"}},
		{"openai chat", chatBase, "/v1/chat/completions", "chat-test", `{"model":"chat-test","messages":[{"role":"user","content":"%s"}]}`, openaiWant, openaiDropped},
		{"codex", codexBase, "/v1/responses", "codex-test", `{"model":"codex-test","input":"%s"}`, openaiWant, openaiDropped},
	} {
		for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
			t.Run(tc.name+" "+http.StatusText(status), func(t *testing.T) {
				prompt := "hi"
				if status == http.StatusTooManyRequests {
					prompt = "trigger-429"
				}
				req, err := http.NewRequest(http.MethodPost, tc.base+tc.path, strings.NewReader(strings.Replace(tc.body, "%s", prompt, 1)))
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
				if resp.StatusCode != status {
					t.Fatalf("status = %d, want %d", resp.StatusCode, status)
				}
				for k, want := range tc.want {
					if got := resp.Header.Get(k); got != want {
						t.Errorf("header %s = %q, want %q", k, got, want)
					}
				}
				for _, k := range tc.dropped {
					if got := resp.Header.Get(k); got != "" {
						t.Errorf("header %s = %q, want it dropped", k, got)
					}
				}
				for k, vs := range resp.Header {
					for _, v := range vs {
						if strings.Contains(v, orgID) || strings.Contains(v, keyPiece) {
							t.Errorf("header %s = %q reached the client", k, v)
						}
					}
				}
			})
		}
	}
}
