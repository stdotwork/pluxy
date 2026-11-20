package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// droidUpstream records the Requests the Codex extension sends while answering Droid traffic.
type droidUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []droidRequest
}

type droidRequest struct {
	path   string
	header http.Header
	body   []byte
}

func newDroidUpstream(t *testing.T, handler http.HandlerFunc) *droidUpstream {
	t.Helper()
	u := &droidUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, droidRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		u.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *droidUpstream) received() []droidRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]droidRequest(nil), u.reqs...)
}

// codexReply is the streamed Responses shape the Codex endpoint returns to a client that asks
// for a non-streamed turn.
func codexReply(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	write := func(event, data string) {
		_, _ = io.WriteString(w, "event: "+event+"\ndata: "+data+"\n\n")
		w.(http.Flusher).Flush()
	}
	write("response.created", `{"type":"response.created","response":{"id":"resp_droid","object":"response","created_at":1700000000,"status":"in_progress","model":"gpt-5-codex","output":[]}}`)
	write("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_droid","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
	write("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_droid","output_index":0,"content_index":0,"delta":"Compact summary."}`)
	write("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_droid","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Compact summary.","annotations":[]}]}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_droid","object":"response","created_at":1700000000,"status":"completed","model":"gpt-5-codex","output":[{"id":"msg_droid","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Compact summary.","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}`)
}

func startDroidCodexCore(t *testing.T, up *droidUpstream) string {
	t.Helper()
	return runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "codex-droid",
				"settings": map[string]any{
					"base_url":       up.URL + "/v1",
					"codex_identity": false,
				},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-droid-factory-e2e"},
			}},
		},
	})
}

// Droid's auto-compress sends a Responses request with a string input. The Codex endpoint takes
// the turn as one user message, and the client gets a completed Response object rather than an
// empty or non-object reply (CLIProxyAPI issues #91, #86 and #1403).
func TestVerifyDroidResponsesStringInput(t *testing.T) {
	up := newDroidUpstream(t, codexReply)
	base := startDroidCodexCore(t, up)
	const body = `{"model":"gpt-5-codex","input":"Summarize this Droid session for auto-compress.","stream":false,"instructions":"Return a compact summary."}`
	resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var reply struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("reply %s is not a Response object: %v", raw, err)
	}
	if reply.Object != "response" || reply.Status != "completed" || len(reply.Output) == 0 ||
		len(reply.Output[0].Content) == 0 || reply.Output[0].Content[0].Type != "output_text" ||
		!strings.Contains(reply.Output[0].Content[0].Text, "Compact summary") {
		t.Fatalf("reply %s lacks a completed output_text Response", raw)
	}

	reqs := up.received()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	if reqs[0].path != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", reqs[0].path)
	}
	var sent struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(reqs[0].body, &sent); err != nil {
		t.Fatalf("upstream body %s: %v", reqs[0].body, err)
	}
	if len(sent.Input) != 1 || sent.Input[0].Type != "message" || sent.Input[0].Role != "user" ||
		len(sent.Input[0].Content) != 1 || sent.Input[0].Content[0].Type != "input_text" ||
		sent.Input[0].Content[0].Text != "Summarize this Droid session for auto-compress." {
		t.Errorf("upstream input = %+v, want one user message carrying the string input", sent.Input)
	}
}

// A Droid Responses turn that replays a Gemini function call and a non-JSON tool result must
// reach GenerateContent as a matching functionCall/functionResponse pair, with the tool output
// carried as an object and the Droid tool schema's JSON Schema-only fields removed (CLIProxyAPI
// issues #123, #451 and #583).
func TestVerifyDroidGeminiToolTurns(t *testing.T) {
	up := newFakeGemini(t, jsonReply(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Read complete."}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"totalTokenCount":15}}`))
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-droid",
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "AIza-droid-factory-e2e"},
			}},
		},
	})
	const body = `{"model":"gemini-2.5-pro","stream":false,"instructions":"You are Droid.","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Read /tmp/droid.txt"}]},` +
		`{"type":"function_call","call_id":"call_read_1","name":"Read","arguments":"{\"file_path\":\"/tmp/droid.txt\"}"},` +
		`{"type":"function_call_output","call_id":"call_read_1","output":"not JSON, just text"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]}],` +
		`"tools":[{"type":"function","name":"Read","parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"],"additionalProperties":false,"$schema":"https://json-schema.org/draft/2020-12/schema"}}]}`
	resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Read complete.") {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}

	seen := up.interactions()[0:0]
	up.mu.Lock()
	for _, r := range up.seen {
		if strings.Contains(r.path, ":generateContent") {
			seen = append(seen, r)
		}
	}
	up.mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("Gemini upstream got %d GenerateContent requests (%s), want 1", len(seen), up.all())
	}
	if seen[0].path != "/v1beta/models/gemini-2.5-pro:generateContent" {
		t.Errorf("upstream path = %q", seen[0].path)
	}
	var sent struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string `json:"text"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(seen[0].body, &sent); err != nil {
		t.Fatalf("upstream body %s: %v", seen[0].body, err)
	}
	var call, result *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Args     json.RawMessage `json:"args"`
		Response json.RawMessage `json:"response"`
	}
	for _, turn := range sent.Contents {
		for _, part := range turn.Parts {
			if part.FunctionCall != nil {
				call = &struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Args     json.RawMessage `json:"args"`
					Response json.RawMessage `json:"response"`
				}{ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Args: part.FunctionCall.Args}
				if part.ThoughtSignature == "" {
					t.Errorf("translated function call has no thoughtSignature: %+v", part)
				}
			}
			if part.FunctionResponse != nil {
				result = &struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Args     json.RawMessage `json:"args"`
					Response json.RawMessage `json:"response"`
				}{ID: part.FunctionResponse.ID, Name: part.FunctionResponse.Name, Response: part.FunctionResponse.Response}
			}
		}
	}
	if call == nil || result == nil {
		t.Fatalf("contents %+v lack a functionCall and functionResponse", sent.Contents)
	}
	if call.ID != "call_read_1" || result.ID != "call_read_1" || call.Name != "Read" || result.Name != "Read" {
		t.Errorf("call = %+v result = %+v, want both IDs call_read_1 and both names Read", call, result)
	}
	var response map[string]any
	if err := json.Unmarshal(result.Response, &response); err != nil || response["result"] != "not JSON, just text" {
		t.Errorf("functionResponse.response = %s, want an object with result text: %v", result.Response, err)
	}
	upstream := string(seen[0].body)
	if strings.Contains(upstream, `"$schema"`) {
		t.Errorf("Gemini upstream kept $schema: %s", upstream)
	}
}

// Droid's OpenAI provider sends POST /responses after being configured with a /v1 base URL.
// The bare route must serve the same Responses turn as /v1/responses (CLIProxyAPI issue #1401).
func TestVerifyDroidBareResponsesAlias(t *testing.T) {
	up := newDroidUpstream(t, codexReply)
	base := startDroidCodexCore(t, up)
	resp := postPath(t, context.Background(), base, "/responses", clientKey, `{"model":"gpt-5-codex","input":"Say hello.","stream":false}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Compact summary.") {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if reqs := up.received(); len(reqs) != 1 {
		t.Errorf("upstream got %d requests, want 1", len(reqs))
	}
}
