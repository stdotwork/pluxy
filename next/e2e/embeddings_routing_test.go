package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A Gemini account serves text embeddings, but cannot take values specific to other
// OpenAI-compatible vendors (token-ID array inputs, unknown encoding_format, more than 2048
// inputs, stream, a thinking suffix, or out-of-range dimensions). When a model is served by a
// Gemini account followed by an OpenAI-compatible account, those requests move on to the
// OpenAI-compatible account; invalid requests stay 400 and end routing; and when only Gemini
// accounts serve the model, the client still gets a clear 400 error naming the field.
func TestGeminiEmbeddingsTokenIDFallback(t *testing.T) {
	const openAIReply = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":3,"total_tokens":3}}`

	geminiUp := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1beta/models/gemini-embedding-001:embedContent" {
			io.WriteString(w, `{"embedding":{"values":[0.25,0.5]},"usageMetadata":{"promptTokenCount":2}}`)
			return
		}
		http.NotFound(w, r)
	})

	type openAIReq struct {
		path string
		body string
	}
	var openAIMu sync.Mutex
	var openAIReqs []openAIReq
	openAIUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost {
			openAIMu.Lock()
			openAIReqs = append(openAIReqs, openAIReq{path: r.URL.Path, body: string(raw)})
			openAIMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, openAIReply)
	}))
	t.Cleanup(openAIUp.Close)

	base, _, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-embed",
				"models":     []string{"gemini-embedding-001"},
				"settings":   map[string]any{"base_url": geminiUp.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "AIza-e2e-99887766"},
			}},
		},
		"extensions": map[string]any{
			"pluxy/openai-compatible": map[string]any{
				"accounts": []any{map[string]any{
					"id":         "openai-embed",
					"models":     []string{"text-embedding-3-small"},
					"settings":   map[string]any{"base_url": openAIUp.URL + "/v1", "wire_api": "chat"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
				}},
			},
		},
		"routing": map[string]any{
			"chains": map[string]any{
				"shared-embed": []any{
					map[string]any{"model": "gemini-embedding-001", "provider": "pluxy/gemini"},
					map[string]any{"model": "text-embedding-3-small", "provider": "pluxy/openai-compatible"},
				},
			},
		},
	}, "-bundled", bundledRegistry(t, map[string]string{
		"pluxy/openai-compatible": "openai-compatible",
	}))
	logs.waitFor(t, "config revision 1 active")
	installBundled(t, base, "pluxy/openai-compatible")

	geminiPosts := func() int {
		geminiUp.mu.Lock()
		defer geminiUp.mu.Unlock()
		n := 0
		for _, req := range geminiUp.seen {
			if req.method == http.MethodPost {
				n++
			}
		}
		return n
	}
	openAICount := func() int {
		openAIMu.Lock()
		defer openAIMu.Unlock()
		return len(openAIReqs)
	}

	t.Run("token array moves on to openai-compatible", func(t *testing.T) {
		beforeOpenAI := openAICount()
		beforeGemini := geminiPosts()
		resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey,
			`{"model":"shared-embed","input":[1212,318,257]}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != openAIReply {
			t.Fatalf("status %d: %s; want 200 with OpenAI-compatible reply", resp.StatusCode, raw)
		}
		if got := geminiPosts() - beforeGemini; got != 0 {
			t.Errorf("Gemini upstream got %d POSTs, want 0", got)
		}
		if got := openAICount() - beforeOpenAI; got != 1 {
			t.Fatalf("OpenAI-compatible upstream got %d requests, want 1", got)
		}
		openAIMu.Lock()
		last := openAIReqs[len(openAIReqs)-1]
		openAIMu.Unlock()
		if last.path != "/v1/embeddings" || !strings.Contains(last.body, `[1212,318,257]`) {
			t.Errorf("OpenAI-compatible upstream got %+v", last)
		}
	})

	t.Run("nested token array moves on to openai-compatible", func(t *testing.T) {
		beforeOpenAI := openAICount()
		beforeGemini := geminiPosts()
		resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey,
			`{"model":"shared-embed","input":[[1212,318],[42]]}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != openAIReply {
			t.Fatalf("status %d: %s; want 200 with OpenAI-compatible reply", resp.StatusCode, raw)
		}
		if got := geminiPosts() - beforeGemini; got != 0 {
			t.Errorf("Gemini upstream got %d POSTs, want 0", got)
		}
		if got := openAICount() - beforeOpenAI; got != 1 {
			t.Fatalf("OpenAI-compatible upstream got %d requests, want 1", got)
		}
	})

	t.Run("text input still served by gemini first", func(t *testing.T) {
		beforeOpenAI := openAICount()
		beforeGemini := geminiPosts()
		resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey,
			`{"model":"shared-embed","input":"hello"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s; want 200 from Gemini", resp.StatusCode, raw)
		}
		if got := geminiPosts() - beforeGemini; got != 1 {
			t.Errorf("Gemini upstream got %d POSTs, want 1", got)
		}
		if got := openAICount() - beforeOpenAI; got != 0 {
			t.Errorf("OpenAI-compatible upstream got %d requests, want 0", got)
		}
	})

	t.Run("unsupported gemini values move on to openai-compatible", func(t *testing.T) {
		manyInputs := make([]string, 2049)
		for i := range manyInputs {
			manyInputs[i] = "a"
		}
		manyBody, _ := json.Marshal(map[string]any{
			"model": "shared-embed",
			"input": manyInputs,
		})
		for _, tc := range []struct {
			name    string
			body    string
			wantSub string
		}{
			{"unknown encoding_format", `{"model":"shared-embed","input":"hi","encoding_format":"int8"}`, `"encoding_format":"int8"`},
			{"more than 2048 inputs", string(manyBody), `"input":[`},
			{"stream", `{"model":"shared-embed","input":"hi","stream":true}`, `"stream":true`},
			{"thinking suffix", `{"model":"shared-embed(high)","input":"hi"}`, `"input":"hi"`},
			{"zero dimensions", `{"model":"shared-embed","input":"hi","dimensions":0}`, `"dimensions":0`},
			{"negative dimensions", `{"model":"shared-embed","input":"hi","dimensions":-1}`, `"dimensions":-1`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				beforeOpenAI := openAICount()
				beforeGemini := geminiPosts()
				resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey, tc.body)
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusOK || string(raw) != openAIReply {
					t.Fatalf("status %d: %s; want 200 with OpenAI-compatible reply", resp.StatusCode, raw)
				}
				if got := geminiPosts() - beforeGemini; got != 0 {
					t.Errorf("Gemini upstream got %d POSTs, want 0", got)
				}
				if got := openAICount() - beforeOpenAI; got != 1 {
					t.Fatalf("OpenAI-compatible upstream got %d requests, want 1", got)
				}
				openAIMu.Lock()
				last := openAIReqs[len(openAIReqs)-1]
				openAIMu.Unlock()
				if last.path != "/v1/embeddings" || !strings.Contains(last.body, tc.wantSub) {
					t.Errorf("OpenAI-compatible upstream got %+v, want path /v1/embeddings and substring %q", last, tc.wantSub)
				}
			})
		}
	})

	t.Run("invalid request stays 400 and does not move on", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
			sub  string
		}{
			{"missing input", `{"model":"shared-embed"}`, "input"},
			{"mixed token and string input", `{"model":"shared-embed","input":[1212,"bad"]}`, "input"},
			{"wrongly typed dimensions", `{"model":"shared-embed","input":"hi","dimensions":"768"}`, "dimensions"},
			{"wrongly typed encoding_format", `{"model":"shared-embed","input":"hi","encoding_format":123}`, "encoding_format"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				beforeOpenAI := openAICount()
				beforeGemini := geminiPosts()
				resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey, tc.body)
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status %d: %s; want 400", resp.StatusCode, raw)
				}
				if !strings.Contains(strings.ToLower(string(raw)), tc.sub) {
					t.Errorf("body %s does not mention %q", raw, tc.sub)
				}
				if got := geminiPosts() - beforeGemini; got != 0 {
					t.Errorf("Gemini upstream got %d POSTs, want 0", got)
				}
				if got := openAICount() - beforeOpenAI; got != 0 {
					t.Errorf("OpenAI-compatible upstream got %d requests, want 0", got)
				}
			})
		}
	})

	t.Run("gemini-only model returns clear 400 naming field", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
			sub  string
		}{
			{"token array", `{"model":"gemini-embedding-001","input":[1212,318,257]}`, "input"},
			{"unknown encoding_format", `{"model":"gemini-embedding-001","input":"hi","encoding_format":"int8"}`, "encoding_format"},
			{"stream", `{"model":"gemini-embedding-001","input":"hi","stream":true}`, "stream"},
			{"thinking suffix", `{"model":"gemini-embedding-001(high)","input":"hi"}`, "thinking"},
			{"out-of-range dimensions", `{"model":"gemini-embedding-001","input":"hi","dimensions":0}`, "dimensions"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp := postPath(t, context.Background(), base, "/v1/embeddings", clientKey, tc.body)
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status %d: %s; want 400", resp.StatusCode, raw)
				}
				var errBody struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(raw, &errBody); err != nil {
					t.Fatalf("reply %s: %v", raw, err)
				}
				if !strings.Contains(strings.ToLower(errBody.Error.Message), tc.sub) {
					t.Errorf("error message = %q, want it to name %q", errBody.Error.Message, tc.sub)
				}
			})
		}
	})
}
