package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// OpenAI POST /v1/embeddings on a Gemini account goes through core to the Gemini extension as
// :embedContent (for a single input) or :batchEmbedContents (for multiple inputs), and returns an
// OpenAI list response with order, index and usage preserved.
func TestGeminiEmbeddings(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1beta/models/gemini-embedding-001:embedContent":
			io.WriteString(w, `{"embedding":{"values":[0.25,0.5,0.75]},"usageMetadata":{"promptTokenCount":4}}`)
		case "/v1beta/models/gemini-embedding-001:batchEmbedContents":
			io.WriteString(w, `{"embeddings":[{"values":[1.0,2.0]},{"values":[3.0,4.0]}],"usageMetadata":{"promptTokenCount":7}}`)
		default:
			http.NotFound(w, r)
		}
	})
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-embed",
				"models":     []string{"gemini-embedding-001"},
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-gemini-embed-key"},
			}},
		},
	})

	t.Run("single", func(t *testing.T) {
		resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey,
			`{"model":"gemini-embedding-001","input":"hello","dimensions":3}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var reply struct {
			Object string `json:"object"`
			Model  string `json:"model"`
			Data   []struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
		if reply.Object != "list" || reply.Model != "gemini-embedding-001" || len(reply.Data) != 1 ||
			reply.Data[0].Object != "embedding" || reply.Data[0].Index != 0 ||
			!reflect.DeepEqual(reply.Data[0].Embedding, []float64{0.25, 0.5, 0.75}) ||
			reply.Usage.PromptTokens != 4 || reply.Usage.TotalTokens != 4 {
			t.Fatalf("reply = %+v", reply)
		}
	})

	t.Run("batch", func(t *testing.T) {
		resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey,
			`{"model":"gemini-embedding-001","input":["first","second"],"dimensions":2}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var reply struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
		if len(reply.Data) != 2 || reply.Data[0].Index != 0 || !reflect.DeepEqual(reply.Data[0].Embedding, []float64{1, 2}) ||
			reply.Data[1].Index != 1 || !reflect.DeepEqual(reply.Data[1].Embedding, []float64{3, 4}) ||
			reply.Usage.PromptTokens != 7 || reply.Usage.TotalTokens != 7 {
			t.Fatalf("reply = %+v", reply)
		}
	})

	up.mu.Lock()
	var posts []seenRequest
	for _, req := range up.seen {
		if req.method == http.MethodPost {
			posts = append(posts, req)
		}
	}
	up.mu.Unlock()
	if len(posts) != 2 {
		t.Fatalf("upstream got %d POST requests, want 2: %s", len(posts), up.all())
	}
	if posts[0].path != "/v1beta/models/gemini-embedding-001:embedContent" {
		t.Errorf("first upstream path = %s", posts[0].path)
	}
	if posts[1].path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
		t.Errorf("second upstream path = %s", posts[1].path)
	}
}

// Gemini client routes POST /v1beta/models/{model}:embedContent and :batchEmbedContents work
// end-to-end through a real core for both Gemini accounts (passthrough) and OpenAI-compatible
// accounts (translated to POST {base_url}/embeddings and back).
func TestGeminiClientEmbedRoutesE2E(t *testing.T) {
	const geminiSingleReply = `{"embedding":{"values":[0.25,0.5]},"usageMetadata":{"promptTokenCount":3}}`
	const geminiBatchReply = `{"embeddings":[{"values":[1.0,2.0]},{"values":[3.0,4.0]}],"usageMetadata":{"promptTokenCount":6}}`

	gemUp := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1beta/models/gemini-embedding-001:embedContent":
			io.WriteString(w, geminiSingleReply)
		case "/v1beta/models/gemini-embedding-001:batchEmbedContents":
			io.WriteString(w, geminiBatchReply)
		default:
			http.NotFound(w, r)
		}
	})

	var oaMu sync.Mutex
	var oaPaths []string
	oaUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		oaMu.Lock()
		oaPaths = append(oaPaths, r.URL.Path)
		oaMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			var req struct {
				Input any `json:"input"`
			}
			_ = json.Unmarshal(body, &req)
			if _, isList := req.Input.([]any); isList {
				io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":1,"embedding":[0.3,0.4]},{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":5,"total_tokens":5}}`)
				return
			}
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.9,0.8]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(oaUp.Close)

	postGoogle := func(base, path, body string) *http.Response {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Goog-Api-Key", clientKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	gemBase := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-embed",
				"models":     []string{"gemini-embedding-001"},
				"settings":   map[string]any{"base_url": gemUp.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-gemini-key"},
			}},
		},
	})

	t.Run("gemini account single passthrough", func(t *testing.T) {
		resp := postGoogle(gemBase, "/v1beta/models/gemini-embedding-001:embedContent", `{"content":{"parts":[{"text":"hello"}]}}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != geminiSingleReply {
			t.Fatalf("status %d: %s, want 200 %s", resp.StatusCode, raw, geminiSingleReply)
		}
	})

	t.Run("gemini account batch passthrough", func(t *testing.T) {
		resp := postGoogle(gemBase, "/v1beta/models/gemini-embedding-001:batchEmbedContents",
			`{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"a"}]}},{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"b"}]}}]}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != geminiBatchReply {
			t.Fatalf("status %d: %s, want 200 %s", resp.StatusCode, raw, geminiBatchReply)
		}
	})

	oaBase := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "openai-embed",
				"models":     []string{"text-embedding-3-small"},
				"settings":   map[string]any{"base_url": oaUp.URL + "/v1", "wire_api": "chat"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})

	t.Run("openai-compatible account single translated", func(t *testing.T) {
		resp := postGoogle(oaBase, "/v1beta/models/text-embedding-3-small:embedContent",
			`{"content":{"parts":[{"text":"hello"}]},"outputDimensionality":2}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var reply struct {
			Embedding struct {
				Values []float64 `json:"values"`
			} `json:"embedding"`
			UsageMetadata struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
		if !reflect.DeepEqual(reply.Embedding.Values, []float64{0.9, 0.8}) || reply.UsageMetadata.PromptTokenCount != 2 {
			t.Fatalf("reply = %+v", reply)
		}
	})

	t.Run("openai-compatible account batch translated", func(t *testing.T) {
		resp := postGoogle(oaBase, "/v1beta/models/text-embedding-3-small:batchEmbedContents",
			`{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var reply struct {
			Embeddings []struct {
				Values []float64 `json:"values"`
			} `json:"embeddings"`
			UsageMetadata struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
		if len(reply.Embeddings) != 2 ||
			!reflect.DeepEqual(reply.Embeddings[0].Values, []float64{0.1, 0.2}) ||
			!reflect.DeepEqual(reply.Embeddings[1].Values, []float64{0.3, 0.4}) ||
			reply.UsageMetadata.PromptTokenCount != 5 {
			t.Fatalf("reply = %+v", reply)
		}
	})
}

// When the first account on a model cannot carry an account-specific embedding field (such as
// taskType on an OpenAI-compatible Chat account, or a non-text part on a Vertex :predict account),
// it answers retryable credential-scoped unsupported so core routes the request to the second
// account on the same model.
func TestGeminiClientEmbedFallbackOnUnsupportedField(t *testing.T) {
	postGoogle := func(base, path, body string) *http.Response {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Goog-Api-Key", clientKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	t.Run("openai-compatible chat falls through to gemini wire on taskType", func(t *testing.T) {
		const wantReply = `{"embedding":{"values":[0.42,0.84]},"usageMetadata":{"promptTokenCount":3}}`
		var mu sync.Mutex
		var paths []string
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1beta/models/gemini-embedding-001:embedContent" {
				io.WriteString(w, wantReply)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(up.Close)

		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"routing":        map[string]any{"strategy": "fill-first"},
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{
					map[string]any{
						"id":         "chat-first",
						"models":     []string{"gemini-embedding-001"},
						"settings":   map[string]any{"base_url": up.URL + "/v1", "wire_api": "chat"},
						"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
					},
					map[string]any{
						"id":         "gemini-second",
						"models":     []string{"gemini-embedding-001"},
						"settings":   map[string]any{"base_url": up.URL + "/v1beta", "wire_api": "gemini"},
						"credential": map[string]any{"kind": "api_key", "api_key": "goog-key"},
					},
				},
			},
		})

		resp := postGoogle(base, "/v1beta/models/gemini-embedding-001:embedContent",
			`{"content":{"parts":[{"text":"hello"}]},"taskType":"RETRIEVAL_QUERY"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != wantReply {
			t.Fatalf("status %d: %s, want 200 %s", resp.StatusCode, raw, wantReply)
		}
		mu.Lock()
		gotPaths := append([]string(nil), paths...)
		mu.Unlock()
		for _, p := range gotPaths {
			if p == "/v1/embeddings" {
				t.Errorf("chat-first should not have called /v1/embeddings; paths = %v", gotPaths)
			}
		}
	})

	t.Run("vertex predict falls through to gemini api on non-text part", func(t *testing.T) {
		const wantReply = `{"embedding":{"values":[0.11,0.22]},"usageMetadata":{"promptTokenCount":4}}`
		up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1beta/models/gemini-embedding-001:embedContent" {
				io.WriteString(w, wantReply)
				return
			}
			http.NotFound(w, r)
		})

		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"routing":        map[string]any{"strategy": "fill-first"},
			"extension": map[string]any{
				"manifest": geminiManifest(t),
				"accounts": []any{
					map[string]any{
						"id":         "vertex-first",
						"models":     []string{"gemini-embedding-001"},
						"settings":   map[string]any{"base_url": up.URL, "vertex": map[string]any{"project": "p", "location": "us-central1"}},
						"credential": map[string]any{"kind": "api_key", "api_key": "vertex-key"},
					},
					map[string]any{
						"id":         "gemini-second",
						"models":     []string{"gemini-embedding-001"},
						"settings":   map[string]any{"base_url": up.URL},
						"credential": map[string]any{"kind": "api_key", "api_key": "gemini-key"},
					},
				},
			},
		})

		resp := postGoogle(base, "/v1beta/models/gemini-embedding-001:embedContent",
			`{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != wantReply {
			t.Fatalf("status %d: %s, want 200 %s", resp.StatusCode, raw, wantReply)
		}
	})
}
