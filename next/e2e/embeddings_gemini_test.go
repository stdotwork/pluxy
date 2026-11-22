package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
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
