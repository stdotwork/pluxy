package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// An OpenAI-compatible account whose upstream lacks a side endpoint answers 404 to it. That fails
// only the request to the endpoint, with a message that names the endpoint: generation for the
// same model on the same account still reaches the upstream. A 404 that says the model does not
// exist still blocks the model, and so does a 404 from the generation path.
func TestOpenAICompatibleSideEndpointMissing(t *testing.T) {
	const (
		chatReply = `{"id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"gpt-test",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		responsesReply = `{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"gpt-test",` +
			`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],` +
			`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		messagesReply = `{"id":"msg_1","type":"message","role":"assistant","model":"gpt-test","content":[{"type":"text","text":"hi"}],` +
			`"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
		geminiReply = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
			`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
		pathNotFound  = `{"detail":"Not Found"}`
		modelNotFound = `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`
	)

	for _, tc := range []struct {
		name string
		wire string
		// side is the client's request to the side endpoint and the path the upstream gets it at.
		sidePath, sideBody, upstreamSide string
		// the message the client gets for a 404 from the side endpoint
		want string
		// generation: the client's request, the path it reaches and the upstream's reply
		genPath, genBody, upstreamGen, genReply string
	}{
		{
			name: "input tokens", wire: "responses",
			sidePath: "/v1/responses/input_tokens", sideBody: `{"model":"gpt-test","input":"hello"}`,
			upstreamSide: "/v1/responses/input_tokens",
			want:         "no input token count endpoint (POST /responses/input_tokens answered 404)",
			genPath:      "/v1/responses", genBody: `{"model":"gpt-test","input":"hello"}`,
			upstreamGen: "/v1/responses", genReply: responsesReply,
		},
		{
			name: "embeddings", wire: "chat",
			sidePath: "/v1/embeddings", sideBody: `{"model":"gpt-test","input":"hello"}`,
			upstreamSide: "/v1/embeddings",
			want:         "no embeddings endpoint (POST /embeddings answered 404)",
			genPath:      "/v1/chat/completions", genBody: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			upstreamGen: "/v1/chat/completions", genReply: chatReply,
		},
		{
			name: "messages count tokens", wire: "messages",
			sidePath: "/v1/messages/count_tokens", sideBody: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			upstreamSide: "/v1/messages/count_tokens",
			want:         "no token count endpoint (POST /messages/count_tokens answered 404)",
			genPath:      "/v1/messages", genBody: `{"model":"gpt-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			upstreamGen: "/v1/messages", genReply: messagesReply,
		},
		{
			name: "gemini countTokens", wire: "gemini",
			sidePath: "/v1beta/models/gpt-test:countTokens", sideBody: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			upstreamSide: "/v1beta/models/gpt-test:countTokens",
			want:         "no token count endpoint (POST /models/gpt-test:countTokens answered 404)",
			genPath:      "/v1beta/models/gpt-test:generateContent", genBody: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			upstreamGen: "/v1beta/models/gpt-test:generateContent", genReply: geminiReply,
		},
		{
			name: "images generations", wire: "chat",
			sidePath: "/v1/images/generations", sideBody: `{"model":"gpt-test","prompt":"a cat"}`,
			upstreamSide: "/v1/images/generations",
			want:         "no images endpoint (POST /images/generations answered 404)",
			genPath:      "/v1/chat/completions", genBody: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			upstreamGen: "/v1/chat/completions", genReply: chatReply,
		},
		{
			name: "images edits", wire: "chat",
			sidePath: "/v1/images/edits", sideBody: `{"model":"gpt-test","prompt":"a cat","images":[{"image_url":"https://example.test/a.png"}]}`,
			upstreamSide: "/v1/images/edits",
			want:         "no images endpoint (POST /images/edits answered 404)",
			genPath:      "/v1/chat/completions", genBody: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			upstreamGen: "/v1/chat/completions", genReply: chatReply,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// run starts a core with one account of the case's wire whose upstream answers the
			// side path with sideStatus and sideReply, and the generation path with genReply
			// unless genStatus says otherwise; it counts the requests each path got.
			run := func(t *testing.T, sideReply string, genStatus int) (string, func(string) int) {
				t.Helper()
				var mu sync.Mutex
				hits := map[string]int{}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"object":"list","data":[{"id":"gpt-test","object":"model"}]}`)
						return
					}
					io.Copy(io.Discard, r.Body)
					mu.Lock()
					hits[r.URL.Path]++
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case tc.upstreamSide:
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, sideReply)
					case tc.upstreamGen:
						w.WriteHeader(genStatus)
						if genStatus == http.StatusOK {
							io.WriteString(w, tc.genReply)
						} else {
							io.WriteString(w, pathNotFound)
						}
					default:
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, pathNotFound)
					}
				}))
				t.Cleanup(srv.Close)
				baseURL := srv.URL + "/v1"
				if tc.wire == "gemini" {
					baseURL = srv.URL + "/v1beta"
				}
				base := runCore(t, map[string]any{
					"listen":         "127.0.0.1:0",
					"client_keys":    []string{clientKey},
					"management_key": managementKey,
					"extension": map[string]any{
						"manifest": filepath.Join(binDir, "pluxy-extension.json"),
						"accounts": []any{map[string]any{
							"id":         "acct",
							"models":     []string{"gpt-test"},
							"settings":   map[string]any{"base_url": baseURL, "wire_api": tc.wire},
							"credential": map[string]any{"kind": "api_key", "api_key": "made-up-sample-9137"},
						}},
					},
				})
				return base, func(path string) int {
					mu.Lock()
					defer mu.Unlock()
					return hits[path]
				}
			}

			t.Run("no side endpoint", func(t *testing.T) {
				base, hits := run(t, pathNotFound, http.StatusOK)
				code, reply := clientPost(t, base, tc.sidePath, tc.sideBody, nil)
				if code != http.StatusNotFound || !strings.Contains(reply, tc.want) {
					t.Errorf("side endpoint: status %d, reply %s; want a 404 saying %q", code, reply, tc.want)
				}
				if got := hits(tc.upstreamSide); got != 1 {
					t.Errorf("upstream got %d requests at %s, want 1", got, tc.upstreamSide)
				}
				code, reply = clientPost(t, base, tc.genPath, tc.genBody, nil)
				if code != http.StatusOK {
					t.Fatalf("generation after the side endpoint's 404: status %d, reply %s; want 200", code, reply)
				}
				if got := hits(tc.upstreamGen); got != 1 {
					t.Errorf("upstream got %d generation requests, want 1", got)
				}
			})

			t.Run("side endpoint says the model does not exist", func(t *testing.T) {
				base, hits := run(t, modelNotFound, http.StatusOK)
				code, _ := clientPost(t, base, tc.sidePath, tc.sideBody, nil)
				if code != http.StatusNotFound {
					t.Errorf("side endpoint: status %d, want 404", code)
				}
				code, reply := clientPost(t, base, tc.genPath, tc.genBody, nil)
				if code == http.StatusOK {
					t.Errorf("generation after a model_not_found: status 200, want the model blocked: %s", reply)
				}
				if got := hits(tc.upstreamGen); got != 0 {
					t.Errorf("upstream got %d generation requests, want 0 while the model is blocked", got)
				}
			})

			t.Run("generation 404 blocks the model", func(t *testing.T) {
				base, hits := run(t, pathNotFound, http.StatusNotFound)
				code, _ := clientPost(t, base, tc.genPath, tc.genBody, nil)
				if code != http.StatusNotFound {
					t.Errorf("first generation: status %d, want 404", code)
				}
				code, _ = clientPost(t, base, tc.genPath, tc.genBody, nil)
				if code == http.StatusOK {
					t.Errorf("second generation: status 200, want the model blocked")
				}
				if got := hits(tc.upstreamGen); got != 1 {
					t.Errorf("upstream got %s generation requests, want 1 (the second is refused by the block)", strconv.Itoa(got))
				}
			})
		})
	}
}
