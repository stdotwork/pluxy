package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Responses compaction on OpenAI-compatible accounts (POST /v1/responses/compact):
//   - wire_api "responses" passes the client's body through to POST {base_url}/responses/compact
//     unchanged and returns the upstream's reply as it came;
//   - wire_api "chat" translates the Responses conversation to one non-streamed
//     POST {base_url}/chat/completions request with tool_choice "none", structured output and stop
//     sequences cleared, and the Codex CLI's compaction prompt appended as a last user message, and
//     returns OpenAI's response.compaction JSON object;
//   - wire_api "messages" refuses with unsupported on the credential before contacting the upstream.
func TestOpenAICompatibleResponsesCompact(t *testing.T) {
	codexHeaders := map[string]string{
		"User-Agent": "codex_cli_rs/0.150.0 (Linux; x86_64)",
		"Originator": "codex_cli_rs",
		"Version":    "0.150.0",
		"Session_id": "s-1",
	}
	reqBody := `{"model":"gpt-4.1","instructions":"You are a coding agent.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
		`"tool_choice":"required","parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
		`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}}}`

	type recordedReq struct {
		method string
		path   string
		header http.Header
		body   []byte
	}
	newUpstream := func(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, func() []recordedReq) {
		t.Helper()
		var mu sync.Mutex
		var reqs []recordedReq
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4.1","object":"model"}]}`)
				return
			}
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			reqs = append(reqs, recordedReq{
				method: r.Method,
				path:   r.URL.Path,
				header: r.Header.Clone(),
				body:   body,
			})
			mu.Unlock()
			handler(w, r, body)
		}))
		t.Cleanup(srv.Close)
		return srv, func() []recordedReq {
			mu.Lock()
			defer mu.Unlock()
			return append([]recordedReq(nil), reqs...)
		}
	}

	t.Run("responses", func(t *testing.T) {
		const (
			apiKey        = "made-up-compat-responses-key"
			upstreamReply = `{"id":"resp_up_compact_1","object":"response.compaction","created_at":1700000000,"model":"gpt-4.1",` +
				`"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_abc"}],` +
				`"usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":10},"output_tokens":45,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":165}}`
		)
		srv, recorded := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, upstreamReply)
		})
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "responses-acct",
					"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": "responses"},
					"credential": map[string]any{"kind": "api_key", "api_key": apiKey},
				}},
			},
		})
		code, reply := clientPost(t, base, "/v1/responses/compact", reqBody, codexHeaders)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, reply)
		}
		if reply != upstreamReply {
			t.Errorf("reply = %s, want %s", reply, upstreamReply)
		}
		reqs := recorded()
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(reqs))
		}
		if reqs[0].method != http.MethodPost || reqs[0].path != "/v1/responses/compact" {
			t.Errorf("upstream got %s %s, want POST /v1/responses/compact", reqs[0].method, reqs[0].path)
		}
		if got := reqs[0].header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q, want %q", got, "Bearer "+apiKey)
		}
		if string(reqs[0].body) != reqBody {
			t.Errorf("upstream body = %s, want unchanged %s", reqs[0].body, reqBody)
		}
	})

	t.Run("chat", func(t *testing.T) {
		const apiKey = "made-up-compat-chat-key"
		srv, recorded := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl_compact_1","object":"chat.completion","created":1700000000,"model":"gpt-4.1",`+
				`"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"Internal thought that must stay hidden.",`+
				`"content":"The user asked for the files; ls showed a.txt."},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":144,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens":276,"completion_tokens_details":{"reasoning_tokens":200},"total_tokens":420}}`)
		})
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "chat-acct",
					"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": "chat"},
					"credential": map[string]any{"kind": "api_key", "api_key": apiKey},
				}},
			},
		})
		code, reply := clientPost(t, base, "/v1/responses/compact", reqBody, codexHeaders)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, reply)
		}
		reqs := recorded()
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(reqs))
		}
		if reqs[0].method != http.MethodPost || reqs[0].path != "/v1/chat/completions" {
			t.Errorf("upstream got %s %s, want POST /v1/chat/completions", reqs[0].method, reqs[0].path)
		}
		if got := reqs[0].header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q, want %q", got, "Bearer "+apiKey)
		}

		var sent struct {
			Model             string          `json:"model"`
			Stream            bool            `json:"stream"`
			ToolChoice        string          `json:"tool_choice"`
			ParallelToolCalls *bool           `json:"parallel_tool_calls"`
			ResponseFormat    json.RawMessage `json:"response_format"`
			Stop              json.RawMessage `json:"stop"`
			ReasoningEffort   string          `json:"reasoning_effort"`
			Tools             json.RawMessage `json:"tools"`
			Messages          []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(reqs[0].body, &sent); err != nil {
			t.Fatalf("upstream body %s: %v", reqs[0].body, err)
		}
		if sent.Stream {
			t.Errorf("upstream request had stream=true: %s", reqs[0].body)
		}
		if sent.ToolChoice != "none" || !strings.Contains(string(sent.Tools), `"shell"`) {
			t.Errorf("tools=%s tool_choice=%q; want shell tool kept with tool_choice none", sent.Tools, sent.ToolChoice)
		}
		if sent.ParallelToolCalls != nil || len(sent.ResponseFormat) != 0 || len(sent.Stop) != 0 {
			t.Errorf("parallel_tool_calls=%v response_format=%s stop=%s; want all cleared", sent.ParallelToolCalls, sent.ResponseFormat, sent.Stop)
		}
		if sent.ReasoningEffort != "high" {
			t.Errorf("reasoning_effort = %q, want high", sent.ReasoningEffort)
		}
		if len(sent.Messages) < 2 || sent.Messages[len(sent.Messages)-1].Role != "user" {
			t.Fatalf("messages = %+v, want trailing user message", sent.Messages)
		}
		if !strings.Contains(string(sent.Messages[len(sent.Messages)-1].Content), "CONTEXT CHECKPOINT COMPACTION") {
			t.Errorf("last user message = %s, want Codex compaction prompt", sent.Messages[len(sent.Messages)-1].Content)
		}

		var got struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Model  string `json:"model"`
			Output []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Status  string `json:"status"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				TotalTokens        int `json:"total_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
				OutputTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(reply), &got); err != nil {
			t.Fatalf("reply %s: %v", reply, err)
		}
		if got.Object != "response.compaction" || !strings.HasPrefix(got.ID, "resp_") || got.Model != "gpt-4.1" {
			t.Errorf("reply %s; want response.compaction with resp_ id and model gpt-4.1", reply)
		}
		if len(got.Output) != 1 || got.Output[0].Type != "message" || got.Output[0].Role != "user" ||
			got.Output[0].Status != "completed" || len(got.Output[0].Content) != 1 || got.Output[0].Content[0].Type != "input_text" {
			t.Fatalf("reply output %s; want one completed user message with one input_text part", reply)
		}
		text := got.Output[0].Content[0].Text
		if !strings.HasPrefix(text, "Another language model started to solve this problem") ||
			!strings.HasSuffix(text, ":\nThe user asked for the files; ls showed a.txt.") {
			t.Errorf("summary text = %q, want Codex summary prefix + newline + summary", text)
		}
		if strings.Contains(text, "Internal thought") {
			t.Errorf("summary text %q leaked reasoning_content", text)
		}
		if got.Usage.InputTokens != 144 || got.Usage.InputTokensDetails.CachedTokens != 20 ||
			got.Usage.OutputTokens != 276 || got.Usage.OutputTokensDetails.ReasoningTokens != 200 ||
			got.Usage.TotalTokens != 420 {
			t.Errorf("usage = %+v, want 144 in (20 cached), 276 out (200 reasoning), 420 total", got.Usage)
		}
	})

	t.Run("messages", func(t *testing.T) {
		srv, recorded := newUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "messages-acct",
					"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": "messages"},
					"credential": map[string]any{"kind": "api_key", "api_key": "made-up-compat-messages-key"},
				}},
			},
		})
		code, _ := clientPost(t, base, "/v1/responses/compact", reqBody, codexHeaders)
		if code == http.StatusOK {
			t.Fatalf("status = 200, want refusal on messages wire_api")
		}
		if got := len(recorded()); got != 0 {
			t.Errorf("upstream got %d requests, want 0", got)
		}
	})
}

// An upstream with no compaction path answers 404 to POST {base_url}/responses/compact. That
// fails only the compaction request, with a message that says so: generation for the same model
// on the same account still reaches the upstream. A 404 from the generation path, or one that
// says the model does not exist, still blocks the model.
func TestOpenAICompatibleCompactPathMissing(t *testing.T) {
	codexHeaders := map[string]string{
		"User-Agent": "codex_cli_rs/0.150.0 (Linux; x86_64)",
		"Originator": "codex_cli_rs",
		"Version":    "0.150.0",
		"Session_id": "s-1",
	}
	compactBody := `{"model":"gpt-4.1","instructions":"You are a coding agent.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]}]}`
	generateBody := `{"model":"gpt-4.1","input":"hello","stream":false}`
	const generated = `{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"gpt-4.1",` +
		`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],` +
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

	// run starts a core with one responses account whose upstream answers each path as paths
	// say (status and body), and counts the requests each path got.
	run := func(t *testing.T, paths map[string][2]string) (string, func(string) int) {
		t.Helper()
		var mu sync.Mutex
		hits := map[string]int{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4.1","object":"model"}]}`)
				return
			}
			io.Copy(io.Discard, r.Body)
			mu.Lock()
			hits[r.URL.Path]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if p, ok := paths[r.URL.Path]; ok {
				code, _ := strconv.Atoi(p[0])
				w.WriteHeader(code)
				io.WriteString(w, p[1])
				return
			}
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"detail":"Not Found"}`)
		}))
		t.Cleanup(srv.Close)
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "responses-acct",
					"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": "responses"},
					"credential": map[string]any{"kind": "api_key", "api_key": "made-up-compat-key"},
				}},
			},
		})
		return base, func(path string) int {
			mu.Lock()
			defer mu.Unlock()
			return hits[path]
		}
	}

	t.Run("no compact path", func(t *testing.T) {
		base, hits := run(t, map[string][2]string{"/v1/responses": {"200", generated}})
		code, reply := clientPost(t, base, "/v1/responses/compact", compactBody, codexHeaders)
		if code != http.StatusNotFound || !strings.Contains(reply, "compaction") {
			t.Errorf("compact: status %d, reply %s; want a 404 that says the upstream has no compaction endpoint", code, reply)
		}
		code, reply = clientPost(t, base, "/v1/responses", generateBody, nil)
		if code != http.StatusOK {
			t.Fatalf("generation after the compact 404: status %d, reply %s; want 200", code, reply)
		}
		if got := hits("/v1/responses"); got != 1 {
			t.Errorf("upstream got %d generation requests, want 1", got)
		}
	})

	t.Run("compact says the model does not exist", func(t *testing.T) {
		base, hits := run(t, map[string][2]string{
			"/v1/responses/compact": {"404", `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`},
			"/v1/responses":         {"200", generated},
		})
		code, _ := clientPost(t, base, "/v1/responses/compact", compactBody, codexHeaders)
		if code != http.StatusNotFound {
			t.Errorf("compact: status %d, want 404", code)
		}
		code, reply := clientPost(t, base, "/v1/responses", generateBody, nil)
		if code == http.StatusOK {
			t.Errorf("generation after a model_not_found: status 200, want the model blocked: %s", reply)
		}
		if got := hits("/v1/responses"); got != 0 {
			t.Errorf("upstream got %d generation requests, want 0 while the model is blocked", got)
		}
	})

	t.Run("generation 404 blocks the model", func(t *testing.T) {
		base, hits := run(t, nil)
		code, _ := clientPost(t, base, "/v1/responses", generateBody, nil)
		if code != http.StatusNotFound {
			t.Errorf("first generation: status %d, want 404", code)
		}
		code, _ = clientPost(t, base, "/v1/responses", generateBody, nil)
		if code == http.StatusOK {
			t.Errorf("second generation: status 200, want the model blocked")
		}
		if got := hits("/v1/responses"); got != 1 {
			t.Errorf("upstream got %d generation requests, want 1 (the second is refused by the block)", got)
		}
	})
}
