package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// responsesUpstream is a small Responses API upstream for client-compatibility checks. It
// records each request, refuses a function_call with no name as the real endpoint does, and
// answers a valid request with the stream shape the Codex extension expects.
type responsesUpstream struct {
	*httptest.Server
	model string

	mu     sync.Mutex
	bodies []map[string]any
}

func newResponsesUpstream(t *testing.T, model string) *responsesUpstream {
	t.Helper()
	u := &responsesUpstream{model: model}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()

		if got, _ := body["model"].(string); got != u.model {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"model %q is not served by this account","type":"invalid_request_error","code":"model_not_found"}}`, got)
			return
		}
		if input, _ := body["input"].([]any); hasEmptyFunctionName(input) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"function call name is empty","type":"invalid_request_error","code":"invalid_function_name"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		event := func(typ string, fields map[string]any) {
			fields["type"] = typ
			b, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
			flush()
		}
		response := map[string]any{
			"id": "resp_verify", "object": "response", "created_at": 1700000000,
			"status": "in_progress", "model": u.model, "output": []any{},
		}
		event("response.created", map[string]any{"response": response})
		event("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{
			"id": "msg_verify", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
		}})
		event("response.output_text.delta", map[string]any{
			"output_index": 0, "content_index": 0, "item_id": "msg_verify", "delta": "Hello from the compatibility upstream.",
		})
		item := map[string]any{
			"id": "msg_verify", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "Hello from the compatibility upstream.", "annotations": []any{}}},
		}
		event("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		response["status"] = "completed"
		response["output"] = []any{item}
		response["usage"] = map[string]any{"input_tokens": 3, "output_tokens": 6, "total_tokens": 9}
		event("response.completed", map[string]any{"response": response})
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *responsesUpstream) received() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.bodies...)
}

func hasEmptyFunctionName(input []any) bool {
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" {
			return true
		}
	}
	return false
}

// A Claude Messages client can replay a tool_use block with an empty name. When its model is
// served by Codex, core must not pass that invalid function call upstream: it either sends a
// request without it, or refuses the request clearly before making any upstream call.
func TestVerifyMessagesEmptyToolNameOnCodex(t *testing.T) {
	up := newResponsesUpstream(t, "gpt-test")
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"management_key": managementKey,
		"client_keys":    []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "codex-a",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-codex-a"},
			}},
		},
	})

	body := `{"model":"gpt-test","max_tokens":128,"messages":[
		{"role":"user","content":"Run the tool named by the previous turn."},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_empty","name":"","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_empty","content":"nothing"},{"type":"text","text":"Say hello."}]}]}`
	resp := postPath(t, t.Context(), base, "/v1/messages", clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	requests := up.received()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode != http.StatusBadRequest || len(requests) != 0 || !strings.Contains(string(raw), "tool") {
			t.Fatalf("status %d after %d upstream calls: %s; want a successful request or a clear client refusal before the upstream", resp.StatusCode, len(requests), raw)
		}
		return
	}
	if !strings.Contains(string(raw), `"type":"message"`) || !strings.Contains(string(raw), "Hello from the compatibility upstream.") {
		t.Fatalf("Messages reply is not a successful message: %s", raw)
	}
	if len(requests) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(requests))
	}
	if hasEmptyFunctionName(requests[0]["input"].([]any)) {
		t.Fatalf("upstream request contains an empty function name: %v", requests[0]["input"])
	}
}

// A Gemini-format request may name a model served by another provider (a gpt-* model on
// Codex). Routing must choose the account that lists that model, not another account merely
// because the client used Gemini's format, and must never forward the request to an account
// that reports a confusing upstream model failure.
func TestVerifyGeminiClientRoutesModelToServingAccount(t *testing.T) {
	other := newResponsesUpstream(t, "gemini-test")
	wanted := newResponsesUpstream(t, "gpt-test")
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"management_key": managementKey,
		"client_keys":    []string{clientKey},
		"routing":        map[string]any{"strategy": "fill-first"},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "gemini-model",
					"models":     []string{"gemini-test"},
					"settings":   map[string]any{"base_url": other.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-other"},
				},
				map[string]any{
					"id":         "gpt-model",
					"models":     []string{"gpt-test"},
					"settings":   map[string]any{"base_url": wanted.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-wanted"},
				},
			},
		},
	})

	resp := postPath(t, t.Context(), base, "/v1beta/models/gpt-test:generateContent", clientKey,
		`{"contents":[{"role":"user","parts":[{"text":"Say hello."}]}]}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	otherRequests, wantedRequests := other.received(), wanted.received()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode != http.StatusBadRequest || len(otherRequests)+len(wantedRequests) != 0 || !strings.Contains(string(raw), "gpt-test") {
			t.Fatalf("status %d after calls to the wrong=%d/right=%d accounts: %s; want routing to the serving account or a clear client refusal", resp.StatusCode, len(otherRequests), len(wantedRequests), raw)
		}
		return
	}
	if !strings.Contains(string(raw), `"candidates"`) || !strings.Contains(string(raw), "Hello from the compatibility upstream.") {
		t.Fatalf("Gemini reply is not a successful candidate: %s", raw)
	}
	if len(otherRequests) != 0 || len(wantedRequests) != 1 {
		t.Fatalf("upstream calls wrong=%d right=%d, want 0 and 1", len(otherRequests), len(wantedRequests))
	}
	if got, _ := wantedRequests[0]["model"].(string); got != "gpt-test" {
		t.Fatalf("serving upstream got model %q, want gpt-test", got)
	}
}
