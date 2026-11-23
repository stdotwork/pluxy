package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const compactSampleBody = `{"model":"gpt-4.1","instructions":"You are a coding agent.",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
	`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
	`"tools":[{"type":"function","name":"shell","description":"Runs a command.","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
	`"tool_choice":"required","stop":["DONE"],"parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
	`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}},"store":false}`

const chatCompactionReply = `{"id":"chatcmpl_compact_1","object":"chat.completion","created":1700000000,"model":"gpt-4.1",` +
	`"choices":[{"index":0,"message":{"role":"assistant",` +
	`"reasoning_content":"Internal reasoning that must be excluded.",` +
	`"content":"The user asked for the files; ls showed a.txt."` +
	`},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":144,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens":276,"completion_tokens_details":{"reasoning_tokens":200},"total_tokens":420}}`

type compactRecorder struct {
	format      string
	contentType string
	body        strings.Builder
	finish      *llm.Finish
	usage       *llm.Usage
}

func (r *compactRecorder) StartBody(format, contentType string) error {
	r.format = format
	r.contentType = contentType
	return nil
}

func (r *compactRecorder) Body(chunk string) error {
	r.body.WriteString(chunk)
	return nil
}

func (r *compactRecorder) Finish(f llm.Finish, usage *llm.Usage) error {
	r.finish = &f
	r.usage = usage
	return nil
}

func compactCall(settings json.RawMessage, apiKey, model, body string) sdk.Call {
	return sdk.Call{
		Account: protocol.Account{
			ID:         "compat-compact",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: apiKey},
		},
		Request: llm.Request{
			Model: model,
			Native: &llm.Native{
				Format:  llm.FormatOpenAIResponsesCompact,
				Body:    json.RawMessage(body),
				Headers: map[string]string{"user-agent": "codex_cli_rs/0.150.0", "originator": "codex_cli_rs"},
			},
		},
	}
}

type seenUpstreamRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

func newCompactUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, func() []seenUpstreamRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []seenUpstreamRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, seenUpstreamRequest{
			method: r.Method,
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   body,
		})
		mu.Unlock()
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenUpstreamRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenUpstreamRequest(nil), reqs...)
	}
}

func TestCompactResponsesPassthrough(t *testing.T) {
	const (
		apiKey        = "made-up-responses-key"
		upstreamReply = `{"id":"resp_native_1","object":"response.compaction","created_at":1700000000,"model":"gpt-4.1",` +
			`"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_123"}],` +
			`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":15},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":130}}`
	)
	srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamReply)
	})
	settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)

	var rec compactRecorder
	if err := compactTo(context.Background(), compactCall(settings, apiKey, "gpt-4.1", compactSampleBody), &rec); err != nil {
		t.Fatalf("compactTo failed: %v", err)
	}
	if rec.format != llm.FormatOpenAIResponsesCompact || rec.contentType != "application/json" || rec.finish == nil || rec.finish.Reason != llm.FinishStop {
		t.Fatalf("rec = %+v; want format %q, application/json, stop finish", rec, llm.FormatOpenAIResponsesCompact)
	}
	if got := rec.body.String(); got != upstreamReply {
		t.Errorf("reply body = %s, want %s", got, upstreamReply)
	}
	if rec.usage == nil || rec.usage.InputTokens == nil || *rec.usage.InputTokens != 100 ||
		rec.usage.CachedInputTokens == nil || *rec.usage.CachedInputTokens != 15 ||
		rec.usage.OutputTokens == nil || *rec.usage.OutputTokens != 30 ||
		rec.usage.ReasoningTokens == nil || *rec.usage.ReasoningTokens != 5 {
		t.Errorf("finish usage = %+v; want 100 in (15 cached), 30 out (5 reasoning)", rec.usage)
	}

	reqs := recorded()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != "/v1/responses/compact" {
		t.Fatalf("upstream requests = %+v, want 1 POST /v1/responses/compact", reqs)
	}
	if got := reqs[0].header.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+apiKey)
	}
	if string(reqs[0].body) != compactSampleBody {
		t.Errorf("upstream body = %s, want unchanged %s", reqs[0].body, compactSampleBody)
	}
}

func TestCompactChatSummary(t *testing.T) {
	t.Run("chat wire summarizes conversation", func(t *testing.T) {
		const apiKey = "made-up-chat-key"
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, chatCompactionReply)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)

		var rec compactRecorder
		if err := compactTo(context.Background(), compactCall(settings, apiKey, "gpt-4.1", compactSampleBody), &rec); err != nil {
			t.Fatalf("compactTo failed: %v", err)
		}
		if rec.format != llm.FormatOpenAIResponsesCompact || rec.finish == nil || rec.finish.Reason != llm.FinishStop {
			t.Fatalf("rec = %+v; want format %q and stop finish", rec, llm.FormatOpenAIResponsesCompact)
		}

		reqs := recorded()
		if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != "/v1/chat/completions" {
			t.Fatalf("upstream requests = %+v, want 1 POST /v1/chat/completions", reqs)
		}
		if got := reqs[0].header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q, want %q", got, "Bearer "+apiKey)
		}

		var sent struct {
			Model             string          `json:"model"`
			Stream            bool            `json:"stream"`
			Tools             json.RawMessage `json:"tools"`
			ToolChoice        string          `json:"tool_choice"`
			ParallelToolCalls *bool           `json:"parallel_tool_calls"`
			Stop              json.RawMessage `json:"stop"`
			ResponseFormat    json.RawMessage `json:"response_format"`
			ReasoningEffort   string          `json:"reasoning_effort"`
			Messages          []struct {
				Role       string          `json:"role"`
				Content    json.RawMessage `json:"content"`
				ToolCalls  json.RawMessage `json:"tool_calls"`
				ToolCallID string          `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(reqs[0].body, &sent); err != nil {
			t.Fatalf("upstream body %s: %v", reqs[0].body, err)
		}
		if sent.Stream {
			t.Errorf("stream = true, want false")
		}
		if sent.Model != "gpt-4.1" {
			t.Errorf("model = %q, want gpt-4.1", sent.Model)
		}
		if !strings.Contains(string(sent.Tools), `"shell"`) {
			t.Errorf("tools = %s, want shell tool declaration", sent.Tools)
		}
		if sent.ToolChoice != "none" {
			t.Errorf("tool_choice = %q, want none", sent.ToolChoice)
		}
		if sent.ParallelToolCalls != nil || len(sent.Stop) != 0 || len(sent.ResponseFormat) != 0 {
			t.Errorf("parallel_tool_calls=%v stop=%s response_format=%s; want all cleared", sent.ParallelToolCalls, sent.Stop, sent.ResponseFormat)
		}
		if sent.ReasoningEffort != "high" {
			t.Errorf("reasoning_effort = %q, want high", sent.ReasoningEffort)
		}
		if len(sent.Messages) < 2 || sent.Messages[len(sent.Messages)-1].Role != "user" {
			t.Fatalf("messages = %+v, want trailing user message", sent.Messages)
		}
		last := sent.Messages[len(sent.Messages)-1]
		if !strings.Contains(string(last.Content), "CONTEXT CHECKPOINT COMPACTION") {
			t.Errorf("last message = %s, want Codex compaction prompt", last.Content)
		}
		allMsgs, _ := json.Marshal(sent.Messages)
		for _, want := range []string{"You are a coding agent.", "List the files.", `"call_1"`, "a.txt", "There is one file, a.txt."} {
			if !strings.Contains(string(allMsgs), want) {
				t.Errorf("messages %s missing %s", allMsgs, want)
			}
		}

		body := rec.body.String()
		var reply struct {
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
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatalf("reply %s: %v", body, err)
		}
		if reply.Object != "response.compaction" || reply.ID != "resp_chatcmpl_compact_1" || reply.Model != "gpt-4.1" {
			t.Errorf("reply %s; want response.compaction, id resp_chatcmpl_compact_1, model gpt-4.1", body)
		}
		if len(reply.Output) != 1 || reply.Output[0].Type != "message" || reply.Output[0].Role != "user" ||
			reply.Output[0].Status != "completed" || len(reply.Output[0].Content) != 1 || reply.Output[0].Content[0].Type != "input_text" {
			t.Fatalf("reply output %s; want one completed user message with input_text", body)
		}
		text := reply.Output[0].Content[0].Text
		if !strings.HasPrefix(text, codexSummaryPrefix+"\n") || !strings.HasSuffix(text, "\nThe user asked for the files; ls showed a.txt.") {
			t.Errorf("summary = %q, want codexSummaryPrefix + newline + summary", text)
		}
		if strings.Contains(text, "Internal reasoning") {
			t.Errorf("summary %q leaked reasoning_content", text)
		}
		if reply.Usage.InputTokens != 144 || reply.Usage.InputTokensDetails.CachedTokens != 20 ||
			reply.Usage.OutputTokens != 276 || reply.Usage.OutputTokensDetails.ReasoningTokens != 200 ||
			reply.Usage.TotalTokens != 420 {
			t.Errorf("reply usage = %+v; want 144 in (20 cached), 276 out (200 reasoning), 420 total", reply.Usage)
		}
		if rec.usage == nil || rec.usage.InputTokens == nil || *rec.usage.InputTokens != 144 ||
			rec.usage.OutputTokens == nil || *rec.usage.OutputTokens != 276 ||
			rec.usage.CachedInputTokens == nil || *rec.usage.CachedInputTokens != 20 ||
			rec.usage.ReasoningTokens == nil || *rec.usage.ReasoningTokens != 200 {
			t.Errorf("finish usage = %+v, want matching counts", rec.usage)
		}
	})

	t.Run("conversation ending on tool output", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, chatCompactionReply)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1"}`)
		body := `{"model":"gpt-4.1","input":[{"type":"message","role":"user","content":"List the files."},` +
			`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"call_1","output":"a.txt"}],` +
			`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]}`
		var rec compactRecorder
		if err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", body), &rec); err != nil {
			t.Fatalf("compactTo failed: %v", err)
		}
		var sent struct {
			ToolChoice string `json:"tool_choice"`
			Messages   []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		raw := recorded()[0].body
		if err := json.Unmarshal(raw, &sent); err != nil || len(sent.Messages) != 4 {
			t.Fatalf("messages = %s (%v), want 4 messages", raw, err)
		}
		if sent.Messages[2].Role != "tool" || sent.Messages[2].ToolCallID != "call_1" || sent.Messages[2].Content != "a.txt" {
			t.Errorf("messages[2] = %+v, want tool output for call_1", sent.Messages[2])
		}
		if sent.Messages[3].Role != "user" || !strings.Contains(sent.Messages[3].Content, "CONTEXT CHECKPOINT COMPACTION") {
			t.Errorf("messages[3] = %+v, want user message with compaction prompt", sent.Messages[3])
		}
		if sent.ToolChoice != "none" {
			t.Errorf("tool_choice = %q, want none", sent.ToolChoice)
		}
	})

	t.Run("thinking suffix overrides reasoning effort", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, chatCompactionReply)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
		call := compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody)
		call.Request.Options.ThinkingSuffix = "low"
		var rec compactRecorder
		if err := compactTo(context.Background(), call, &rec); err != nil {
			t.Fatalf("compactTo failed: %v", err)
		}
		if !strings.Contains(string(recorded()[0].body), `"reasoning_effort":"low"`) {
			t.Errorf("body = %s, want reasoning_effort low from suffix", recorded()[0].body)
		}
	})
}

func TestCompactWithoutSummary(t *testing.T) {
	cases := []struct {
		name, reply, want string
	}{
		{"length empty", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`, "length"},
		{"length partial", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"cut off"},"finish_reason":"length"}]}`, "length"},
		{"content_filter", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`, "content_filter"},
		{"tool_calls finish", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "tool_calls"},
		{"reasoning only on stop", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"only thought","content":""},"finish_reason":"stop"}]}`, "stop"},
		{"tool call on stop", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"stop"}]}`, "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			})
			settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
			var rec compactRecorder
			err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
			var e *llm.Error
			if !errors.As(err, &e) || e.Retryable || e.Scope != llm.ScopeRequest || !strings.Contains(e.Message, tc.want) {
				t.Errorf("err = %+v; want non-retryable ScopeRequest naming %q", err, tc.want)
			}
			if e.Usage != nil {
				t.Errorf("err.Usage = %+v, want nil when reply has no usage", e.Usage)
			}
		})
	}
}

func TestCompactWithoutSummaryKeepsUsage(t *testing.T) {
	const fullUsage = `"usage":{"prompt_tokens":144,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens":276,"completion_tokens_details":{"reasoning_tokens":200},"total_tokens":420}`
	cases := []struct {
		name, reply string
	}{
		{"length partial", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"cut off"},"finish_reason":"length"}],` + fullUsage + `}`},
		{"content_filter", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}],` + fullUsage + `}`},
		{"reasoning only on stop", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"only thought","content":""},"finish_reason":"stop"}],` + fullUsage + `}`},
		{"tool call on stop", `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"stop"}],` + fullUsage + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			})
			settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
			var rec compactRecorder
			err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
			var e *llm.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *llm.Error", err)
			}
			if e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != 144 ||
				e.Usage.OutputTokens == nil || *e.Usage.OutputTokens != 276 ||
				e.Usage.CachedInputTokens == nil || *e.Usage.CachedInputTokens != 20 ||
				e.Usage.ReasoningTokens == nil || *e.Usage.ReasoningTokens != 200 {
				t.Errorf("err.Usage = %+v; want 144 in (20 cached), 276 out (200 reasoning)", e.Usage)
			}
		})
	}
}

func TestCompactUpstreamErrorsAndUnsupported(t *testing.T) {
	t.Run("rate limit on chat wire maps through upstreamError", func(t *testing.T) {
		srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
		var rec compactRecorder
		err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.RetryAfterMS != 7000 {
			t.Errorf("err = %+v; want retryable CodeRateLimit on ScopeModel with 7000ms", err)
		}
	})

	t.Run("auth error on responses wire maps through upstreamError", func(t *testing.T) {
		srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)
		var rec compactRecorder
		err := compactTo(context.Background(), compactCall(settings, "sk-bad", "gpt-4.1", compactSampleBody), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("err = %+v; want retryable CodeAuth on ScopeCredential", err)
		}
	})

	t.Run("a 404 from the compact path fails only the request", func(t *testing.T) {
		for name, body := range map[string]string{
			"no body":                     ``,
			"a path not found":            `{"detail":"Not Found"}`,
			"a message without the model": `{"error":{"message":"no route for POST /v1/responses/compact","type":"invalid_request_error"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
					w.WriteHeader(http.StatusNotFound)
					io.WriteString(w, body)
				})
				settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)
				var rec compactRecorder
				err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
				var e *llm.Error
				if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Scope != llm.ScopeRequest || e.Retryable || e.Status != http.StatusNotFound ||
					!strings.Contains(e.Message, "no compaction endpoint") {
					t.Errorf("err = %+v; want a non-retryable CodeNotFound on ScopeRequest saying the upstream has no compaction endpoint", err)
				}
			})
		}
	})

	t.Run("a 404 that says the model does not exist keeps the model block", func(t *testing.T) {
		for name, body := range map[string]string{
			"code":    `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`,
			"message": `{"error":{"message":"model gpt-4.1 not found","type":"invalid_request_error"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
					w.WriteHeader(http.StatusNotFound)
					io.WriteString(w, body)
				})
				settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`)
				var rec compactRecorder
				err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
				var e *llm.Error
				if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Scope != llm.ScopeModel || !e.Retryable {
					t.Errorf("err = %+v; want a retryable CodeNotFound on ScopeModel", err)
				}
			})
		}
	})

	t.Run("a 404 from the generation path of a chat compaction keeps the model block", func(t *testing.T) {
		srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"detail":"Not Found"}`)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
		var rec compactRecorder
		err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Scope != llm.ScopeModel || strings.Contains(e.Message, "no compaction endpoint") {
			t.Errorf("err = %+v; want CodeNotFound on ScopeModel, as for any generation", err)
		}
	})

	t.Run("messages and gemini wires answer unsupported on credential", func(t *testing.T) {
		for _, wire := range []string{wireMessages, wireGemini} {
			t.Run(wire, func(t *testing.T) {
				srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
					w.WriteHeader(http.StatusInternalServerError)
				})
				settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"` + wire + `"}`)
				var rec compactRecorder
				err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", compactSampleBody), &rec)
				var e *llm.Error
				if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
					t.Errorf("%s err = %+v; want retryable CodeUnsupported on ScopeCredential", wire, err)
				}
				if len(recorded()) != 0 {
					t.Errorf("%s upstream got %d requests, want 0", wire, len(recorded()))
				}
			})
		}
	})

	t.Run("unknown endpoint format answers unsupported on credential", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		call := compactCall(json.RawMessage(`{"base_url":"`+srv.URL+`/v1"}`), "sk-test", "gpt-4.1", `{}`)
		call.Request.Native.Format = llm.FormatOpenAIImagesGenerations
		var rec compactRecorder
		err := compactTo(context.Background(), call, &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("err = %+v; want retryable CodeUnsupported on ScopeCredential", err)
		}
		if len(recorded()) != 0 {
			t.Errorf("upstream got %d requests, want 0", len(recorded()))
		}
	})

	t.Run("invalid request body never reaches chat upstream", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`)
		var rec compactRecorder
		err := compactTo(context.Background(), compactCall(settings, "sk-test", "gpt-4.1", `{"model":`), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeRequest {
			t.Errorf("err = %+v, want invalid_request on ScopeRequest", err)
		}
		if len(recorded()) != 0 {
			t.Errorf("upstream got %d requests, want 0", len(recorded()))
		}
	})
}

func TestCompactionHelpers(t *testing.T) {
	req, err := compactionRequest(llm.Request{
		Model:      "gpt-4.1",
		DropPolicy: llm.DropReject,
		Caller:     "c-1",
		Options:    llm.Options{ThinkingSuffix: "medium"},
		Native:     &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(compactSampleBody), Headers: map[string]string{"x-test": "1"}},
	})
	if err != nil {
		t.Fatalf("compactionRequest: %v", err)
	}
	if req.Stream || req.Model != "gpt-4.1" || req.Options.Stop != nil || req.Options.ResponseFormat != nil ||
		req.Options.ParallelToolCalls != nil || req.Options.ToolChoice == nil || req.Options.ToolChoice.Mode != llm.ToolChoiceNone ||
		req.Options.ThinkingSuffix != "medium" || req.DropPolicy != llm.DropReject || req.Caller != "c-1" {
		t.Errorf("unexpected normalized request: %+v", req.Options)
	}
	last := req.Items[len(req.Items)-1]
	if last.Role != llm.RoleUser || len(last.Content) != 1 || last.Content[0].Text != codexSummarizationPrompt {
		t.Errorf("last item = %+v, want user codexSummarizationPrompt", last)
	}

	reply, _, err := compactionReply([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), "gpt-4.1", 1700000000)
	if err != nil {
		t.Fatalf("compactionReply: %v", err)
	}
	if !strings.Contains(string(reply), `"id":"resp_1700000000"`) {
		t.Errorf("reply = %s, want fallback id resp_1700000000", reply)
	}

	_, _, err = compactionReply([]byte(`not json`), "gpt-4.1", 1700000000)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
		t.Errorf("malformed reply err = %+v, want CodeUpstream", err)
	}
}

func TestNoEndpoint(t *testing.T) {
	const want = "this account's upstream has no token count endpoint (POST /models/m:countTokens answered 404)"
	notFound := func(retryable bool) *llm.Error {
		return &llm.Error{Code: llm.CodeNotFound, Scope: llm.ScopeModel, Status: http.StatusNotFound, Message: "Not Found", Retryable: retryable}
	}
	t.Run("a 404 fails only the request", func(t *testing.T) {
		err := noEndpoint(notFound(false), "token count", "/models/m:countTokens")
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Scope != llm.ScopeRequest || e.Retryable || e.Status != http.StatusNotFound || e.Message != want {
			t.Errorf("err = %+v; want a non-retryable CodeNotFound on ScopeRequest saying %q", err, want)
		}
	})
	t.Run("the compaction message is unchanged", func(t *testing.T) {
		err := noEndpoint(notFound(false), "compaction", "/responses/compact")
		var e *llm.Error
		if !errors.As(err, &e) || e.Message != "this account's upstream has no compaction endpoint (POST /responses/compact answered 404)" {
			t.Errorf("err = %+v; want the compaction endpoint message", err)
		}
	})
	t.Run("a 404 that says the model does not exist is left alone", func(t *testing.T) {
		err := noEndpoint(notFound(true), "token count", "/models/m:countTokens")
		var e *llm.Error
		if !errors.As(err, &e) || e.Scope != llm.ScopeModel || !e.Retryable || e.Message != "Not Found" {
			t.Errorf("err = %+v; want the retryable model-scoped 404 unchanged", err)
		}
	})
	t.Run("other errors are left alone", func(t *testing.T) {
		other := &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, Retryable: true, Message: "slow down"}
		if got := noEndpoint(other, "token count", "/x"); got != error(other) || other.Scope != llm.ScopeModel || other.Message != "slow down" {
			t.Errorf("got %v, other = %+v; want the rate limit unchanged", got, other)
		}
		if got := noEndpoint(nil, "token count", "/x"); got != nil {
			t.Errorf("noEndpoint(nil) = %v, want nil", got)
		}
	})
}

// A count path the vendor lacks says the account cannot count, so core can answer with its estimate;
// a 404 that names an unknown model stays what it was.
func TestNoCountEndpoint(t *testing.T) {
	var e *llm.Error
	err := noCountEndpoint(&llm.Error{Code: llm.CodeNotFound, Status: 404, Scope: llm.ScopeModel}, "token count", "/x")
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || e.Retryable || e.Status != 404 {
		t.Errorf("missing path = %+v, want a non-retryable unsupported on the credential, status 404", err)
	}
	model := &llm.Error{Code: llm.CodeNotFound, Status: 404, Scope: llm.ScopeModel, Retryable: true}
	if got := noCountEndpoint(model, "token count", "/x"); got != error(model) || model.Code != llm.CodeNotFound || model.Scope != llm.ScopeModel {
		t.Errorf("unknown model = %+v, want it unchanged", got)
	}
}
