package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// An embeddings request reaches a Chat-wire OpenAI-compatible account's POST
// {base_url}/embeddings, with the key sent as for Chat, and the upstream's reply reaches the
// client unchanged.
func TestEmbeddingsThroughOpenAICompatible(t *testing.T) {
	const reply = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5,0.75]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":3,"total_tokens":3}}`

	type request struct {
		method, path, authorization string
		body                        []byte
	}
	var mu sync.Mutex
	var got []request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, request{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, reply)
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":       "embeddings",
				"models":   []string{"text-embedding-3-small"},
				"settings": map[string]any{"base_url": up.URL + "/v1", "wire_api": "chat"},
				"credential": map[string]any{
					"kind": "api_key", "api_key": upstreamKey,
				},
			}},
		},
	})

	body := `{"model":"text-embedding-3-small","input":["first","second"],"encoding_format":"float"}`
	resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != reply {
		t.Fatalf("status %d: %s; want 200 with the upstream's reply unchanged", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want the upstream's application/json; charset=utf-8", ct)
	}

	mu.Lock()
	defer mu.Unlock()
	var paths []string
	for _, r := range got {
		paths = append(paths, r.method+" "+r.path)
	}
	var embedding *request
	for i := range got {
		if got[i].path == "/v1/embeddings" {
			embedding = &got[i]
			break
		}
	}
	if embedding == nil {
		t.Fatalf("upstream got %v, want POST /v1/embeddings", paths)
	}
	r := *embedding
	if r.method != http.MethodPost || r.path != "/v1/embeddings" {
		t.Errorf("upstream request = %s %s, want POST /v1/embeddings", r.method, r.path)
	}
	if r.authorization != "Bearer "+upstreamKey {
		t.Errorf("upstream Authorization = %q, want the account's key as a bearer token", r.authorization)
	}
	if string(r.body) != body {
		t.Errorf("upstream body = %s, want %s", r.body, body)
	}
}
