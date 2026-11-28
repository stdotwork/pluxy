package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const compactBody = `{"model":"claude-opus-4-6","instructions":"You are a coding agent.",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
	`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
	`"tools":[{"type":"function","name":"shell","description":"Runs a command.","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
	`"tool_choice":"required","parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
	`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}},"store":false}`

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
	if end.Usage == nil || end.Usage.InputTokens == nil || *end.Usage.InputTokens != 170 ||
		end.Usage.OutputTokens == nil || *end.Usage.OutputTokens != 276 ||
		end.Usage.CachedInputTokens == nil || *end.Usage.CachedInputTokens != 20 ||
		end.Usage.CacheWriteInputTokens == nil || *end.Usage.CacheWriteInputTokens != 6 {
		t.Errorf("finish usage %+v; want 170 in, 276 out, 20 cached, 6 cache write", end.Usage)
	}
}

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

func TestCompactionReplyUsage(t *testing.T) {
	base := `{"id":"msg_u","type":"message","role":"assistant","model":"claude-opus-4-6",` +
		`"content":[{"type":"compaction","content":"Short summary."}],"stop_reason":"compaction"`

	t.Run("iterations with cache writes", func(t *testing.T) {
		raw := base + `,"usage":{"input_tokens":0,"output_tokens":0,"iterations":[` +
			`{"type":"compaction","input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":4},` +
			`{"type":"compaction","input_tokens":44,"output_tokens":226,"cache_read_input_tokens":10,"cache_creation_input_tokens":2}]}}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.InputTokens == nil || *u.InputTokens != 170 ||
			u.OutputTokens == nil || *u.OutputTokens != 276 ||
			u.CachedInputTokens == nil || *u.CachedInputTokens != 20 ||
			u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 6 {
			t.Fatalf("usage = %+v; want 170 in, 276 out, 20 cached, 6 cache write", u)
		}
	})

	t.Run("iterations without cache fields leave cache counts nil", func(t *testing.T) {
		raw := base + `,"usage":{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":144,"output_tokens":276}]}}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.InputTokens == nil || *u.InputTokens != 144 ||
			u.OutputTokens == nil || *u.OutputTokens != 276 ||
			u.CachedInputTokens != nil || u.CacheWriteInputTokens != nil {
			t.Fatalf("usage = %+v; want 144 in, 276 out, nil cached, nil cache write", u)
		}
	})

	t.Run("top-level usage without iterations", func(t *testing.T) {
		raw := base + `,"usage":{"input_tokens":50,"output_tokens":25,"cache_creation_input_tokens":12}}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.InputTokens == nil || *u.InputTokens != 62 ||
			u.OutputTokens == nil || *u.OutputTokens != 25 ||
			u.CachedInputTokens != nil ||
			u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 12 {
			t.Fatalf("usage = %+v; want 62 in, 25 out, nil cached, 12 cache write", u)
		}
	})

	t.Run("missing usage returns nil", func(t *testing.T) {
		raw := base + `}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u != nil {
			t.Fatalf("usage = %+v; want nil when upstream omitted usage", u)
		}
	})

	t.Run("null usage returns nil", func(t *testing.T) {
		raw := base + `,"usage":null}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u != nil {
			t.Fatalf("usage = %+v; want nil when upstream sent null usage", u)
		}
	})

	t.Run("empty usage object leaves all counts nil", func(t *testing.T) {
		raw := base + `,"usage":{}}`
		_, u, err := compactionReply([]byte(raw), "claude-opus-4-6", 1700000000)
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.InputTokens != nil || u.OutputTokens != nil || u.CachedInputTokens != nil || u.CacheWriteInputTokens != nil {
			t.Fatalf("usage = %+v; want all counts nil for empty usage object", u)
		}
	})
}

func TestCompactErrorObjectOn200(t *testing.T) {
	t.Run("through extension", func(t *testing.T) {
		const errBody = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_123"}`
		srv := claudetest.New(t)
		srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, errBody)
		}
		ext := startExtension(t)
		evs := events(t, ext.Endpoint(context.Background(), compactAccount(srv), compactRequest(compactBody)))
		end := evs[len(evs)-1]
		if end.Type != llm.EventError || end.Error == nil {
			t.Fatalf("call ended with %+v, want an error", end)
		}
		if end.Error.Code != llm.CodeUpstream || end.Error.ProviderCode != "overloaded_error" || end.Error.Message != "Overloaded" {
			t.Errorf("error = %+v; want upstream overloaded_error with message Overloaded", end.Error)
		}
		if string(end.Error.Raw) != errBody {
			t.Errorf("error raw = %s; want %s", end.Error.Raw, errBody)
		}
	})

	t.Run("context overflow error on 200", func(t *testing.T) {
		const errBody = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`
		_, _, err := compactionReply([]byte(errBody), "claude-opus-4-6", 1700000000)
		e, ok := err.(*llm.Error)
		if !ok || e == nil {
			t.Fatalf("err = %v, want *llm.Error", err)
		}
		if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest ||
			e.Scope != llm.ScopeRequest || e.ProviderCode != llm.ProviderContextLengthExceeded {
			t.Errorf("error = %+v; want 400 invalid_request context_length_exceeded on request scope", e)
		}
	})

	t.Run("error object without message uses fallback", func(t *testing.T) {
		const errBody = `{"type":"error","error":{}}`
		_, _, err := compactionReply([]byte(errBody), "claude-opus-4-6", 1700000000)
		e, ok := err.(*llm.Error)
		if !ok || e == nil {
			t.Fatalf("err = %v, want *llm.Error", err)
		}
		if e.Code != llm.CodeUpstream || e.Message != "upstream reported an error" || string(e.Raw) != errBody {
			t.Errorf("error = %+v; want upstream fallback message and raw body", e)
		}
	})
}

func TestCompactErrorObjectOn200IsAboutTheRequest(t *testing.T) {
	raw := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad compaction request"}}`)
	_, _, err := compactionReply(raw, "claude-sonnet-4-6", 1)
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *llm.Error", err)
	}
	if e.Scope != llm.ScopeRequest {
		t.Errorf("scope = %q, want %q", e.Scope, llm.ScopeRequest)
	}
}
