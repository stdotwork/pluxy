package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
)

// The Codex CLI's compaction on a Claude account: POST /v1/responses/compact, sent as the Codex
// CLI sends it, reaches Anthropic as one on-demand compaction request (the compaction parameter,
// its beta in anthropic-beta, not streamed), and the client gets OpenAI's compaction reply with
// Claude's summary in its output.
func TestClaudeResponsesCompact(t *testing.T) {
	srv := claudetest.New(t)
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_compact1","type":"message","role":"assistant","model":"claude-opus-4-6",`+
			`"content":[{"type":"compaction","content":"The user asked for the files; ls showed a.txt.","signature":"EuYBCkQYexample"}],`+
			`"stop_reason":"compaction","stop_sequence":null,`+
			`"usage":{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":144,"output_tokens":276}]}}`)
	}
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "key", "settings": srv.Settings(""),
				"credential": map[string]any{"kind": "api_key", "api_key": srv.IssueKey()},
				"models":     []string{"claude-opus-4-6"},
			}},
		},
	})
	body := `{"model":"claude-opus-4-6","instructions":"You are a coding agent.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}],` +
		`"parallel_tool_calls":false,"reasoning":{"effort":"high"}}`
	code, reply := clientPost(t, base, "/v1/responses/compact", body, map[string]string{
		"User-Agent": "codex_cli_rs/0.150.0 (Linux; x86_64)", "Originator": "codex_cli_rs", "Version": "0.150.0", "Session_id": "s-1",
	})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, reply)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	if beta := reqs[0].Header.Get("Anthropic-Beta"); !strings.Contains(beta, "compact-2026-09-04") {
		t.Errorf("upstream anthropic-beta %q, want compact-2026-09-04 in it", beta)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
		t.Fatalf("upstream body %s: %v", reqs[0].Body, err)
	}
	if string(sent["compaction"]) != `{"type":"summarize"}` || string(sent["stream"]) == "true" ||
		!strings.Contains(string(sent["messages"]), "There is one file, a.txt.") {
		t.Errorf("upstream body %s; want the conversation with compaction {\"type\":\"summarize\"}, not streamed", reqs[0].Body)
	}

	var got struct {
		Object string `json:"object"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(reply), &got); err != nil {
		t.Fatalf("reply %s: %v", reply, err)
	}
	if got.Object != "response.compaction" || len(got.Output) != 1 || got.Output[0].Type != "message" || got.Output[0].Role != "user" ||
		len(got.Output[0].Content) != 1 || !strings.HasSuffix(got.Output[0].Content[0].Text, "\nThe user asked for the files; ls showed a.txt.") {
		t.Errorf("reply %s; want a response.compaction whose output is one user message ending in Claude's summary", reply)
	}
	if got.Usage.InputTokens != 144 || got.Usage.OutputTokens != 276 {
		t.Errorf("reply usage %+v; want 144 in, 276 out", got.Usage)
	}
}
