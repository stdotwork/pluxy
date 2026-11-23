package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A chat model answers an embedding call with a 404 that names it. That must not block the
// model's generation on any account, and the embedding moves on to another account that may serve it.
func TestEmbedding404DoesNotBlockGeneration(t *testing.T) {
	const model = "gemini-2.5-flash"
	const notEmbeddable = `{"error":{"code":404,"message":"models/gemini-2.5-flash is not found for API version v1beta, or is not supported for embedContent.","status":"NOT_FOUND"}}`
	const generated = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	const chatReply = `{"id":"c","object":"chat.completion","model":"gemini-2.5-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

	post := func(t *testing.T, base, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Goog-Api-Key", clientKey)
		req.Header.Set("Authorization", "Bearer "+clientKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	geminiGenerate := func(t *testing.T, base string) {
		t.Helper()
		code, body := post(t, base, "/v1beta/models/"+model+":generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		if code != http.StatusOK {
			t.Fatalf("generateContent after an embedding 404: status %d: %s, want 200", code, body)
		}
	}
	embedRoutes := []struct{ name, path, body string }{
		{"embedContent", "/v1beta/models/" + model + ":embedContent", `{"content":{"parts":[{"text":"hello"}]}}`},
		{"batchEmbedContents", "/v1beta/models/" + model + ":batchEmbedContents", `{"requests":[{"content":{"parts":[{"text":"hello"}]}}]}`},
		{"v1 embeddings", "/v1/embeddings", `{"model":"` + model + `","input":"hello"}`},
	}
	geminiAccount := func(id, baseURL string) map[string]any {
		return map[string]any{
			"id": id, "models": []string{model},
			"settings":   map[string]any{"base_url": baseURL},
			"credential": map[string]any{"kind": "api_key", "api_key": "fake-gemini-key"},
		}
	}

	t.Run("gemini accounts", func(t *testing.T) {
		for _, rt := range embedRoutes {
			t.Run(rt.name, func(t *testing.T) {
				var embeds atomic.Int32
				up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "mbed") {
						embeds.Add(1)
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, notEmbeddable)
						return
					}
					io.WriteString(w, generated)
				})
				base := runCore(t, map[string]any{
					"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
					"extension": map[string]any{"manifest": geminiManifest(t), "accounts": []any{
						geminiAccount("one", up.URL), geminiAccount("two", up.URL),
					}},
				})
				geminiGenerate(t, base)
				code, body := post(t, base, rt.path, rt.body)
				if code < 400 || code == http.StatusTooManyRequests {
					t.Fatalf("embedding answered %d: %s, want its own failure", code, body)
				}
				if n := embeds.Load(); n != 2 {
					t.Errorf("the embedding reached the upstream %d times, want 2 (both accounts tried)", n)
				}
				geminiGenerate(t, base)
			})
		}
	})

	t.Run("embedding moves on to an account that serves it", func(t *testing.T) {
		var generates atomic.Int32
		refusing := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "mbed") {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, notEmbeddable)
				return
			}
			generates.Add(1)
			io.WriteString(w, generated)
		})
		serving := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"embedding":{"values":[0.5]}}`)
		})
		base := runCore(t, map[string]any{
			"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
			"routing": map[string]any{"strategy": "fill-first"},
			"extension": map[string]any{"manifest": geminiManifest(t), "accounts": []any{
				geminiAccount("refusing", refusing.URL), geminiAccount("serving", serving.URL),
			}},
		})
		code, body := post(t, base, embedRoutes[0].path, embedRoutes[0].body)
		if code != http.StatusOK || !strings.Contains(body, `"values":[0.5]`) {
			t.Fatalf("embedding: status %d: %s, want the second account's 200", code, body)
		}
		before := generates.Load()
		geminiGenerate(t, base)
		if n := generates.Load() - before; n != 1 {
			t.Errorf("the refusing account served %d generations, want 1: the embedding 404 must not block it", n)
		}
	})

	t.Run("openai-compatible gemini wire", func(t *testing.T) {
		for _, rt := range embedRoutes[:2] {
			t.Run(rt.name, func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "mbed") {
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, notEmbeddable)
						return
					}
					io.WriteString(w, generated)
				}))
				t.Cleanup(up.Close)
				base := runCore(t, map[string]any{
					"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
					"extension": map[string]any{"manifest": filepath.Join(binDir, "pluxy-extension.json"), "accounts": []any{map[string]any{
						"id": "oa-gemini", "models": []string{model},
						"settings":   map[string]any{"base_url": up.URL + "/v1beta", "wire_api": "gemini"},
						"credential": map[string]any{"kind": "api_key", "api_key": "goog-key"},
					}}},
				})
				code, body := post(t, base, rt.path, rt.body)
				if code < 400 || code == http.StatusTooManyRequests {
					t.Fatalf("embedding answered %d: %s, want its own failure", code, body)
				}
				geminiGenerate(t, base)
			})
		}
	})

	t.Run("openai-compatible chat wire", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/embeddings" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":{"message":"The model `+"`gemini-2.5-flash`"+` does not exist","type":"invalid_request_error","code":"model_not_found"}}`)
				return
			}
			io.WriteString(w, chatReply)
		}))
		t.Cleanup(up.Close)
		base := runCore(t, map[string]any{
			"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
			"extension": map[string]any{"manifest": filepath.Join(binDir, "pluxy-extension.json"), "accounts": []any{map[string]any{
				"id": "oa-chat", "models": []string{model},
				"settings":   map[string]any{"base_url": up.URL + "/v1", "wire_api": "chat"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}}},
		})
		for _, rt := range []struct{ path, body string }{
			{"/v1/embeddings", `{"model":"` + model + `","input":"hello"}`},
			{embedRoutes[0].path, embedRoutes[0].body},
		} {
			code, body := post(t, base, rt.path, rt.body)
			if code < 400 || code == http.StatusTooManyRequests {
				t.Fatalf("%s answered %d: %s, want its own failure", rt.path, code, body)
			}
		}
		code, body := post(t, base, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if code != http.StatusOK {
			t.Fatalf("chat after an embedding 404: status %d: %s, want 200", code, body)
		}
	})
}
