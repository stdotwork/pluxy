package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// Responses input token counting: OpenAI-compatible Responses accounts and Codex API-key
// accounts pass the request to {base_url}/responses/input_tokens; an account whose upstream
// cannot count answers from core's estimate in OpenAI's reply shape.
func TestResponsesInputTokens(t *testing.T) {
	const (
		requestBody  = `{"model":"gpt-test","instructions":"Be brief.","input":[{"role":"user","content":[{"type":"input_text","text":"Say hello"}]}],"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
		upstreamBody = `{"object":"response.input_tokens","input_tokens":321}`
	)
	type recorded struct {
		path, auth string
		body       []byte
	}
	newUpstream := func(t *testing.T) (*httptest.Server, func() []recorded) {
		t.Helper()
		var mu sync.Mutex
		var got []recorded
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Method == http.MethodPost && r.URL.Path == "/v1/responses/input_tokens" {
				mu.Lock()
				got = append(got, recorded{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, upstreamBody)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv, func() []recorded {
			mu.Lock()
			defer mu.Unlock()
			return append([]recorded(nil), got...)
		}
	}

	for _, tc := range []struct {
		name, manifest string
		settings       func(string) map[string]any
	}{
		{
			name:     "openai-compatible responses",
			manifest: filepath.Join(binDir, "pluxy-extension.json"),
			settings: func(base string) map[string]any {
				return map[string]any{"base_url": base + "/v1", "wire_api": "responses"}
			},
		},
		{
			name:     "codex api key",
			manifest: filepath.Join(binDir, "codex", "pluxy-extension.json"),
			settings: func(base string) map[string]any {
				return map[string]any{"base_url": base + "/v1"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, requests := newUpstream(t)
			base := runCore(t, map[string]any{
				"listen":         "127.0.0.1:0",
				"client_keys":    []string{clientKey},
				"management_key": managementKey,
				"extension": map[string]any{
					"manifest": tc.manifest,
					"accounts": []any{map[string]any{
						"id":         "tokens",
						"models":     []string{"gpt-test"},
						"settings":   tc.settings(up.URL),
						"credential": map[string]any{"kind": "api_key", "api_key": "sk-token-count"},
					}},
				},
			})
			code, reply := clientPost(t, base, "/v1/responses/input_tokens", requestBody, nil)
			if code != http.StatusOK || reply != upstreamBody {
				t.Fatalf("answered %d: %s", code, reply)
			}
			got := requests()
			if len(got) != 1 || got[0].path != "/v1/responses/input_tokens" || got[0].auth != "Bearer sk-token-count" || string(got[0].body) != requestBody {
				t.Fatalf("upstream requests = %+v, want one unchanged POST /v1/responses/input_tokens with the account key", got)
			}
		})
	}

	t.Run("estimate", func(t *testing.T) {
		up, requests := newUpstream(t)
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "chat-wire",
					"models":     []string{"gpt-test"},
					"settings":   map[string]any{"base_url": up.URL + "/v1", "wire_api": "chat"},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-chat-wire"},
				}},
			},
		})
		const want = `{"input_tokens":27,"object":"response.input_tokens"}`
		code, reply := clientPost(t, base, "/v1/responses/input_tokens", requestBody, nil)
		if code != http.StatusOK || reply != want {
			t.Fatalf("answered %d: %s, want %s", code, reply, want)
		}
		if got := requests(); len(got) != 0 {
			t.Fatalf("the upstream saw %+v, want no request from an account that cannot count", got)
		}
	})
}
