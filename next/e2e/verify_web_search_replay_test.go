package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
)

// startClaudeWebSearchFailoverCore runs core with two Claude API-key accounts on one fake
// Anthropic. Requests rotate over the accounts in the configured order.
func startClaudeWebSearchFailoverCore(t *testing.T, srv *claudetest.Server, keyA, keyB string) string {
	t.Helper()
	settings := srv.Settings("")
	return runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"management_key": managementKey,
		"client_keys":    []string{clientKey},
		"routing":        map[string]any{"strategy": "round-robin"},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id": "credential-a", "settings": settings, "models": []string{"claude-opus-4-6"},
					"credential": map[string]any{"kind": "api_key", "api_key": keyA},
				},
				map[string]any{
					"id": "credential-b", "settings": settings, "models": []string{"claude-opus-4-6"},
					"credential": map[string]any{"kind": "api_key", "api_key": keyB},
				},
			},
		},
	})
}

func writeClaudeWebSearchReply(w http.ResponseWriter, encrypted string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_search_a","type":"message","role":"assistant","model":"claude-opus-4-6",`+
		`"content":[`+
		`{"type":"server_tool_use","id":"srvtoolu_a","name":"web_search","input":{"query":"pluxy web search replay"}},`+
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_a","content":[`+
		`{"type":"web_search_result","title":"Search result","url":"https://example.com/result","encrypted_content":"`+encrypted+`","page_age":"1 day"}]},`+
		`{"type":"text","text":"The search result is available."}],`+
		`"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":8}}`)
}

func writeClaudeTextReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"msg_replay","type":"message","role":"assistant","model":"claude-opus-4-6",`+
		`"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","stop_sequence":null,`+
		`"usage":{"input_tokens":12,"output_tokens":8}}`, text)
}

func writeClaudeCredentialError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": message}})
	w.Write(b)
}

func claudeRequestKey(r *http.Request) string {
	return r.Header.Get("X-Api-Key")
}

// A Responses client's history can carry a web_search_result_location annotation whose
// encrypted_index was minted for another Claude credential. Replaying it on the next account must
// not send that opaque value back to Claude, so the conversation still continues.
func TestVerifyWebSearchReplayForeignCitationsDoNotFailCredentialFailover(t *testing.T) {
	const badIndex = "encrypted-index-minted-for-a"
	srv := claudetest.New(t)
	keyA, keyB := srv.IssueKey(), srv.IssueKey()
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		if claudeRequestKey(r) == keyB && strings.Contains(string(req.Body), badIndex) {
			writeClaudeCredentialError(w, "messages.1.content.1.citations.0: Invalid `encrypted_index` in `text` block")
			return
		}
		writeClaudeTextReply(w, "The cited answer remains usable.")
	}
	base := startClaudeWebSearchFailoverCore(t, srv, keyA, keyB)

	body, err := json.Marshal(map[string]any{
		"model":  "claude-opus-4-6",
		"stream": false,
		"tools":  []any{map[string]any{"type": "web_search"}},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "Find the pluxy result."},
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{
					"type": "output_text", "text": "The result is cited below.",
					"annotations": []any{map[string]any{
						"type": "web_search_result_location", "url": "https://example.com/result",
						"title": "Search result", "encrypted_index": badIndex, "cited_text": "result",
					}},
				},
			}},
			map[string]any{"type": "message", "role": "user", "content": "Continue from the earlier answer."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for turn, wantAccount := range []string{"a", "b"} {
		code, reply := clientPost(t, base, "/v1/responses", string(body), nil)
		if code != http.StatusOK || !strings.Contains(reply, "The cited answer remains usable.") {
			t.Fatalf("turn %d on credential %s: status %d, body %s", turn+1, wantAccount, code, reply)
		}
	}
	requests := srv.Requests()
	if len(requests) != 2 {
		t.Fatalf("upstream got %d Messages requests, want 2", len(requests))
	}
	if gotA, gotB := requests[0].Header.Get("X-Api-Key"), requests[1].Header.Get("X-Api-Key"); gotA != keyA || gotB != keyB {
		t.Fatalf("upstream credentials = %q then %q, want A then B", gotA, gotB)
	}
	if strings.Contains(string(requests[1].Body), badIndex) {
		t.Fatalf("the request sent credential A's encrypted_index to credential B: %s", requests[1].Body)
	}
}

// A Responses history can keep a web_search_call's encrypted result from credential A. When the
// next turn is routed to credential B, Claude refuses that result and the conversation cannot
// continue. The same history on credential A must still carry its result, so the test does not
// pass by discarding every replayed search.
func TestVerifyWebSearchReplayForeignSearchResultsDoNotFailCredentialFailover(t *testing.T) {
	t.Skip("bug tro-web-search-replay-credential-failover: replayed search results minted for another credential are sent back unchanged")
	const encryptedA = "encrypted-content-minted-for-a"
	srv := claudetest.New(t)
	keyA, keyB := srv.IssueKey(), srv.IssueKey()
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		key := claudeRequestKey(r)
		body := string(req.Body)
		replay := strings.Contains(body, `"web_search_tool_result"`)
		switch {
		case !replay:
			if key != keyA {
				writeClaudeCredentialError(w, "the first turn did not reach credential A")
				return
			}
			writeClaudeWebSearchReply(w, encryptedA)
		case key == keyA:
			if !strings.Contains(body, encryptedA) {
				writeClaudeCredentialError(w, "the replay on credential A lost its search result")
				return
			}
			writeClaudeTextReply(w, "The search result is still here.")
		case key == keyB:
			if strings.Contains(body, encryptedA) {
				writeClaudeCredentialError(w, "messages.1.content.1: Invalid `encrypted_content` in `search_result` block")
				return
			}
			writeClaudeTextReply(w, "The conversation continued after failover.")
		default:
			http.Error(w, "unexpected credential", http.StatusUnauthorized)
		}
	}
	base := startClaudeWebSearchFailoverCore(t, srv, keyA, keyB)

	firstBody, _ := json.Marshal(map[string]any{
		"model":  "claude-opus-4-6",
		"stream": false,
		"tools":  []any{map[string]any{"type": "web_search"}},
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "Search for the pluxy result."}},
		}},
	})
	code, reply := clientPost(t, base, "/v1/responses", string(firstBody), nil)
	if code != http.StatusOK {
		t.Fatalf("first turn: status %d, body %s", code, reply)
	}
	var first struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal([]byte(reply), &first); err != nil {
		t.Fatalf("first reply: %v: %s", err, reply)
	}
	var searchRaw json.RawMessage
	var search map[string]any
	for _, raw := range first.Output {
		var item map[string]any
		if json.Unmarshal(raw, &item) == nil && item["type"] == "web_search_call" {
			searchRaw, search = raw, item
			break
		}
	}
	results, _ := search["results"].([]any)
	if search == nil || len(results) != 1 || !strings.Contains(string(searchRaw), encryptedA) {
		t.Fatalf("first reply has no web_search_call with its encrypted result: %s", reply)
	}

	input := []any{map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Search for the pluxy result."}},
	}}
	for _, item := range first.Output {
		input = append(input, item)
	}
	input = append(input, map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Continue using the earlier search."}},
	})
	replayBody, _ := json.Marshal(map[string]any{
		"model":  "claude-opus-4-6",
		"stream": false,
		"tools":  []any{map[string]any{"type": "web_search"}},
		"input":  input,
	})

	code, reply = clientPost(t, base, "/v1/responses", string(replayBody), nil)
	if code != http.StatusOK || !strings.Contains(reply, "The conversation continued after failover.") {
		requests := srv.Requests()
		t.Fatalf("replay after failover: status %d, body %s; upstream requests %d", code, reply, len(requests))
	}
	// The third round returns to credential A. A broad fix that strips every replayed result
	// would fail here, even though it made the failover request pass.
	code, reply = clientPost(t, base, "/v1/responses", string(replayBody), nil)
	if code != http.StatusOK || !strings.Contains(reply, "The search result is still here.") {
		t.Fatalf("replay on the minting credential: status %d, body %s", code, reply)
	}
	requests := srv.Requests()
	if len(requests) != 3 {
		t.Fatalf("upstream got %d Messages requests, want 3", len(requests))
	}
	if gotA, gotB, gotA2 := requests[0].Header.Get("X-Api-Key"), requests[1].Header.Get("X-Api-Key"), requests[2].Header.Get("X-Api-Key"); gotA != keyA || gotB != keyB || gotA2 != keyA {
		t.Fatalf("upstream credentials = %q, %q, %q; want A, B, A", gotA, gotB, gotA2)
	}
}
