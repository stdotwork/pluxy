package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// compactBody is a Responses compaction request as the Codex CLI sends one: instructions, a
// conversation with a tool call and its output, the tools, a reasoning effort and fields only
// OpenAI's Responses API reads, with a forced tool choice and structured output added.
const compactBody = `{"model":"claude-opus-4-6","instructions":"You are a coding agent.",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
	`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
	`"tools":[{"type":"function","name":"shell","description":"Runs a command.","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
	`"tool_choice":"required","parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
	`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}},"store":false}`

// anthropicCompaction is Anthropic's reply to an on-demand compaction request, as its docs show it:
// one compaction block, stop_reason compaction, and the summarization call's usage in
// iterations (the top-level counts are zero).
const anthropicCompaction = `{"id":"msg_compact1","type":"message","role":"assistant","model":"claude-opus-4-6",` +
	`"content":[{"type":"compaction","content":"The user asked for the files; ls showed a.txt.","signature":"EuYBCkQYexample"}],` +
	`"stop_reason":"compaction","stop_sequence":null,` +
	`"usage":{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":144,"output_tokens":276,"cache_read_input_tokens":20,"cache_creation_input_tokens":6}]}}`

func compactAccount(srv *claudetest.Server) protocol.Account {
	return protocol.Account{ID: "claude", Settings: srv.Settings(""), Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}}
}

func compactRequest(body string) llm.Request {
	return llm.Request{Model: "claude-opus-4-6", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(body),
		Headers: map[string]string{"user-agent": "codex_cli_rs/0.150.0", "originator": "codex_cli_rs"}}}
}

// replyBody returns the body an endpoint call wrote, its format, and the terminal event.
func replyBody(t *testing.T, evs []llm.Event) (body, format string, end llm.Event) {
	t.Helper()
	var b strings.Builder
	for _, ev := range evs {
		switch ev.Type {
		case llm.EventResponseStart:
			format = ev.Response.Format
		case llm.EventNative:
			var piece string
			if err := json.Unmarshal(ev.Raw, &piece); err != nil {
				t.Fatalf("native event %s is not a JSON string", ev.Raw)
			}
			b.WriteString(piece)
		}
	}
	return b.String(), format, evs[len(evs)-1]
}

// A Responses compaction request on a Claude account becomes one on-demand compaction request
// to the Messages API: the conversation translated as for /v1/responses (instructions as the
// system prompt, the tool call and its output, the tools), "compaction": {"type": "summarize"},
// not streamed, the compaction beta in anthropic-beta, and without what Anthropic refuses on a
// compaction request (a forced tool choice and structured output). The reply is OpenAI's
// compaction reply: one JSON object whose output holds Claude's summary as the user message the
// Codex CLI keeps, with the summarization call's usage.
func TestCompactThroughExtension(t *testing.T) {
	srv := claudetest.New(t)
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicCompaction)
	}
	ext := startExtension(t)
	evs := events(t, ext.Endpoint(context.Background(), compactAccount(srv), compactRequest(compactBody)))
	body, format, end := replyBody(t, evs)
	if end.Type != llm.EventFinish {
		t.Fatalf("call ended with %+v, want a finished reply", end)
	}
	if format != llm.FormatOpenAIResponsesCompact {
		t.Errorf("reply format %q, want %q", format, llm.FormatOpenAIResponsesCompact)
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
	if !jsonEqual(sent["compaction"], json.RawMessage(`{"type":"summarize"}`)) {
		t.Errorf("upstream compaction = %s, want {\"type\":\"summarize\"}", sent["compaction"])
	}
	if s := string(sent["stream"]); s == "true" {
		t.Errorf("upstream stream = %s, want no stream", s)
	}
	var tc struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(sent["tool_choice"], &tc) == nil && (tc.Type == "any" || tc.Type == "tool") {
		t.Errorf("upstream tool_choice = %s, want no forced tool choice", sent["tool_choice"])
	}
	for _, f := range []string{"stop_sequences", "context_management", "betas"} {
		if v, ok := sent[f]; ok {
			t.Errorf("upstream %s = %s, want it left out", f, v)
		}
	}
	var oc map[string]json.RawMessage
	_ = json.Unmarshal(sent["output_config"], &oc)
	if v, ok := oc["format"]; ok {
		t.Errorf("upstream output_config.format = %s, want it left out", v)
	}
	if !strings.Contains(string(sent["system"]), "You are a coding agent.") {
		t.Errorf("upstream system = %s, want the instructions", sent["system"])
	}
	for _, want := range []string{`"tool_use"`, `"tool_result"`, "List the files.", "There is one file, a.txt."} {
		if !strings.Contains(string(sent["messages"]), want) {
			t.Errorf("upstream messages %s, want %s in them", sent["messages"], want)
		}
	}
	var msgs []struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(sent["messages"], &msgs) != nil || len(msgs) == 0 || msgs[len(msgs)-1].Role != "assistant" {
		t.Errorf("upstream messages %s; want them to end on the assistant's reply", sent["messages"])
	}
	if !strings.Contains(string(sent["tools"]), `"shell"`) {
		t.Errorf("upstream tools = %s, want the shell tool", sent["tools"])
	}

	var reply struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
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
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("reply %s is not JSON: %v", body, err)
	}
	if reply.Object != "response.compaction" || reply.ID == "" || reply.Model != "claude-opus-4-6" {
		t.Errorf("reply %s; want object response.compaction, an id and the model", body)
	}
	if len(reply.Output) != 1 || reply.Output[0].Type != "message" || reply.Output[0].Role != "user" ||
		len(reply.Output[0].Content) != 1 || reply.Output[0].Content[0].Type != "input_text" {
		t.Fatalf("reply output %s; want one user message with one input_text part", body)
	}
	if text := reply.Output[0].Content[0].Text; !strings.HasPrefix(text, "Another language model started to solve this problem") ||
		!strings.HasSuffix(text, ":\nThe user asked for the files; ls showed a.txt.") {
		t.Errorf("summary message %q; want the Codex CLI's summary prefix, a newline and Claude's summary", text)
	}
	u := reply.Usage
	if u.InputTokens != 170 || u.OutputTokens != 276 || u.TotalTokens != 446 || u.InputTokensDetails.CachedTokens != 20 {
		t.Errorf("reply usage %+v; want 170 in (20 cached), 276 out, 446 total", u)
	}
	if end.Usage == nil || end.Usage.InputTokens == nil || *end.Usage.InputTokens != 170 || end.Usage.OutputTokens == nil || *end.Usage.OutputTokens != 276 {
		t.Errorf("finish usage %+v; want 170 in, 276 out", end.Usage)
	}
}

// When the summarization call ends without a summary (here at max_tokens, which Anthropic
// answers with a 200 and empty content), the client gets an error naming why, not an empty
// compaction, and the account is not blamed.
func TestCompactWithoutSummary(t *testing.T) {
	srv := claudetest.New(t)
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_x","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":"max_tokens",`+
			`"usage":{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":144,"output_tokens":4096}]}}`)
	}
	ext := startExtension(t)
	evs := events(t, ext.Endpoint(context.Background(), compactAccount(srv), compactRequest(compactBody)))
	end := evs[len(evs)-1]
	if end.Type != llm.EventError || end.Error == nil {
		t.Fatalf("call ended with %+v, want an error", end)
	}
	if end.Error.Retryable || end.Error.Scope != llm.ScopeRequest || !strings.Contains(end.Error.Message, "max_tokens") {
		t.Errorf("error %+v; want one about this request, not retried, naming max_tokens", end.Error)
	}
}

// An endpoint the extension does not serve is refused so that core tries another account, and
// nothing goes upstream.
func TestEndpointNotServed(t *testing.T) {
	srv := claudetest.New(t)
	ext := startExtension(t)
	req := llm.Request{Model: "gpt-image-2", Native: &llm.Native{Format: llm.FormatOpenAIImagesGenerations, Body: json.RawMessage(`{"model":"gpt-image-2","prompt":"a cat"}`)}}
	evs := events(t, ext.Endpoint(context.Background(), compactAccount(srv), req))
	end := evs[len(evs)-1]
	if end.Type != llm.EventError || end.Error.Code != llm.CodeUnsupported || end.Error.Scope != llm.ScopeCredential || !end.Error.Retryable {
		t.Errorf("call ended with %+v; want unsupported, scope credential, retryable", end)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

// compactionBody adds the compaction parameter and its beta, takes out what Anthropic refuses
// on a compaction request, and leaves an automatic tool choice alone.
func TestCompactionBody(t *testing.T) {
	in := `{"model":"m","max_tokens":10,"stream":true,"stop_sequences":["x"],"tool_choice":{"type":"any"},` +
		`"context_management":{"edits":[]},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{}}},"messages":[]}`
	got, err := compactionBody([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","max_tokens":10,"output_config":{"effort":"high"},"messages":[],"compaction":{"type":"summarize"},"betas":["compact-2026-09-04"]}`
	if !jsonEqual(got, json.RawMessage(want)) {
		t.Errorf("got %s\nwant %s", got, want)
	}
	got, err = compactionBody([]byte(`{"tool_choice":{"type":"auto"},"output_config":{"format":{"type":"json_schema"}},"betas":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"tool_choice":{"type":"auto"},"compaction":{"type":"summarize"},"betas":["a","compact-2026-09-04"]}`; !jsonEqual(got, json.RawMessage(want)) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// A conversation that ends on a tool's output (a user turn on Claude) goes as it is.
func TestCompactEndingOnToolOutput(t *testing.T) {
	srv := claudetest.New(t)
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicCompaction)
	}
	ext := startExtension(t)
	body := `{"model":"claude-opus-4-6","input":[{"type":"message","role":"user","content":"List the files."},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"a.txt"}],` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]}`
	evs := events(t, ext.Endpoint(context.Background(), compactAccount(srv), compactRequest(body)))
	if end := evs[len(evs)-1]; end.Type != llm.EventFinish {
		t.Fatalf("call ended with %+v, want a finished reply", end)
	}
	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	got := srv.Requests()[0].Body
	if json.Unmarshal(got, &sent) != nil || len(sent.Messages) != 3 || sent.Messages[2].Role != "user" ||
		len(sent.Messages[2].Content) != 1 || sent.Messages[2].Content[0].Type != "tool_result" {
		t.Errorf("upstream body %s; want the conversation ending on the tool result", got)
	}
}
