package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const compactSampleBody = `{"model":"gemini-2.5-pro","instructions":"You are a coding agent.",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
	`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
	`"tools":[{"type":"function","name":"shell","description":"Runs a command.","parameters":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
	`"tool_choice":"required","stop":["DONE"],"parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
	`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}},"store":false}`

const geminiCompactionReply = `{"responseId":"gem_compact_1","modelVersion":"gemini-2.5-pro",` +
	`"candidates":[{"content":{"role":"model","parts":[` +
	`{"text":"Internal reasoning that must be excluded.","thought":true},` +
	`{"text":"The user asked for the files; ls showed a.txt."}` +
	`]},"finishReason":"STOP","index":0}],` +
	`"usageMetadata":{"promptTokenCount":144,"cachedContentTokenCount":20,"candidatesTokenCount":76,"thoughtsTokenCount":200}}`

func compactCall(settings json.RawMessage, cred *protocol.Credential, model, body string) sdk.Call {
	return sdk.Call{
		Account: protocol.Account{ID: "gemini-compact", Settings: settings, Credential: cred},
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

func compactReplyBody(t *testing.T, rec *recorder) (format, body string, finish llm.Event) {
	t.Helper()
	var b strings.Builder
	for _, ev := range rec.events {
		switch ev.Type {
		case llm.EventResponseStart:
			if ev.Response != nil {
				format = ev.Response.Format
			}
		case llm.EventNative:
			var piece string
			if err := json.Unmarshal(ev.Raw, &piece); err != nil {
				t.Fatalf("native event %s is not a JSON string", ev.Raw)
			}
			b.WriteString(piece)
		case llm.EventFinish:
			finish = ev
		}
	}
	return format, b.String(), finish
}

func TestCompactThroughExtension(t *testing.T) {
	t.Run("gemini api key", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, geminiCompactionReply)
		}
		cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
		settings := json.RawMessage(`{"base_url":"` + srv.URL + `"}`)

		var rec recorder
		if err := compactTo(context.Background(), compactCall(settings, cred, "gemini-2.5-pro", compactSampleBody), &rec); err != nil {
			t.Fatalf("compactTo failed: %v", err)
		}
		format, body, finish := compactReplyBody(t, &rec)
		if format != llm.FormatOpenAIResponsesCompact || finish.Type != llm.EventFinish {
			t.Fatalf("format=%q finish=%+v; want %q finish", format, finish, llm.FormatOpenAIResponsesCompact)
		}

		reqs := srv.Requests()
		if len(reqs) != 1 || reqs[0].Stream || reqs[0].Kind != "gemini" {
			t.Fatalf("upstream requests = %+v, want 1 non-streamed gemini request", reqs)
		}

		var sent struct {
			SystemInstruction json.RawMessage `json:"systemInstruction"`
			Contents          []struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"contents"`
			Tools      json.RawMessage `json:"tools"`
			ToolConfig struct {
				FunctionCallingConfig struct {
					Mode string `json:"mode"`
				} `json:"functionCallingConfig"`
			} `json:"toolConfig"`
			GenerationConfig struct {
				StopSequences    []string        `json:"stopSequences"`
				ResponseMimeType string          `json:"responseMimeType"`
				ResponseSchema   json.RawMessage `json:"responseSchema"`
				ThinkingConfig   struct {
					IncludeThoughts bool   `json:"includeThoughts"`
					ThinkingBudget  *int   `json:"thinkingBudget"`
					ThinkingLevel   string `json:"thinkingLevel"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
			t.Fatalf("upstream body %s: %v", reqs[0].Body, err)
		}
		if !strings.Contains(string(sent.SystemInstruction), "You are a coding agent.") {
			t.Errorf("systemInstruction = %s, want instructions", sent.SystemInstruction)
		}
		if strings.Contains(string(sent.Tools), `"$schema"`) || !strings.Contains(string(sent.Tools), `"shell"`) {
			t.Errorf("tools = %s, want cleaned shell declaration without $schema", sent.Tools)
		}
		if sent.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
			t.Errorf("toolConfig = %+v, want functionCallingConfig.mode NONE", sent.ToolConfig)
		}
		if len(sent.GenerationConfig.StopSequences) != 0 || sent.GenerationConfig.ResponseMimeType != "" || len(sent.GenerationConfig.ResponseSchema) != 0 {
			t.Errorf("generationConfig = %+v, want stopSequences and responseFormat cleared", sent.GenerationConfig)
		}
		if sent.GenerationConfig.ThinkingConfig.ThinkingBudget == nil || *sent.GenerationConfig.ThinkingConfig.ThinkingBudget != 24576 {
			t.Errorf("thinkingConfig = %+v, want thinkingBudget 24576 for effort high", sent.GenerationConfig.ThinkingConfig)
		}
		if len(sent.Contents) < 2 || sent.Contents[len(sent.Contents)-1].Role != "user" {
			t.Fatalf("contents = %+v, want trailing user turn", sent.Contents)
		}
		last, _ := json.Marshal(sent.Contents[len(sent.Contents)-1].Parts)
		if !strings.Contains(string(last), "CONTEXT CHECKPOINT COMPACTION") {
			t.Errorf("last turn = %s, want Codex compaction prompt", last)
		}
		allContents, _ := json.Marshal(sent.Contents)
		for _, want := range []string{`"functionCall"`, `"functionResponse"`, `"call_1"`, "List the files.", "There is one file, a.txt."} {
			if !strings.Contains(string(allContents), want) {
				t.Errorf("contents %s missing %s", allContents, want)
			}
		}

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
		if reply.Object != "response.compaction" || reply.ID != "resp_gem_compact_1" || reply.Model != "gemini-2.5-pro" {
			t.Errorf("reply %s; want response.compaction, id resp_gem_compact_1, model gemini-2.5-pro", body)
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
			t.Errorf("summary %q leaked thought part", text)
		}
		if reply.Usage.InputTokens != 144 || reply.Usage.InputTokensDetails.CachedTokens != 20 ||
			reply.Usage.OutputTokens != 276 || reply.Usage.OutputTokensDetails.ReasoningTokens != 200 ||
			reply.Usage.TotalTokens != 420 {
			t.Errorf("reply usage = %+v; want 144 in (20 cached), 276 out (200 reasoning), 420 total", reply.Usage)
		}
		if finish.Usage == nil || finish.Usage.InputTokens == nil || *finish.Usage.InputTokens != 144 ||
			finish.Usage.OutputTokens == nil || *finish.Usage.OutputTokens != 276 ||
			finish.Usage.CachedInputTokens == nil || *finish.Usage.CachedInputTokens != 20 ||
			finish.Usage.ReasoningTokens == nil || *finish.Usage.ReasoningTokens != 200 {
			t.Errorf("finish usage = %+v, want matching counts", finish.Usage)
		}
	})

	t.Run("vertex service account and express key", func(t *testing.T) {
		for _, kind := range []string{kindVertexSA, kindVertexKey} {
			t.Run(kind, func(t *testing.T) {
				srv := geminitest.New(t)
				srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, geminiCompactionReply)
				}
				var cred *protocol.Credential
				wantKind := "vertex-key"
				if kind == kindVertexSA {
					cred = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("compact-proj", true)}
					wantKind = "vertex"
				} else {
					cred = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
				}
				var rec recorder
				if err := compactTo(context.Background(), compactCall(settingsFor(srv, kind), cred, "gemini-3-flash-preview", compactSampleBody), &rec); err != nil {
					t.Fatalf("compactTo(%s): %v", kind, err)
				}
				reqs := srv.Requests()
				if len(reqs) != 1 || reqs[0].Kind != wantKind || reqs[0].Stream {
					t.Fatalf("requests = %+v, want 1 non-streamed %s", reqs, wantKind)
				}
				if strings.Contains(string(reqs[0].Body), `"call_1"`) {
					t.Errorf("%s retained call_1 id: %s", kind, reqs[0].Body)
				}
				if !strings.Contains(string(reqs[0].Body), `"thinkingLevel":"high"`) {
					t.Errorf("%s lost thinkingLevel high for gemini-3-flash-preview: %s", kind, reqs[0].Body)
				}
			})
		}
	})

	t.Run("thinking suffix overrides reasoning effort", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, geminiCompactionReply)
		}
		cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
		call := compactCall(settingsFor(srv, kindGemini), cred, "gemini-3-flash-preview", compactSampleBody)
		call.Request.Options.ThinkingSuffix = "low"
		var rec recorder
		if err := compactTo(context.Background(), call, &rec); err != nil {
			t.Fatalf("compactTo failed: %v", err)
		}
		if !strings.Contains(string(srv.Requests()[0].Body), `"thinkingLevel":"low"`) {
			t.Errorf("body = %s, want thinkingLevel low from suffix", srv.Requests()[0].Body)
		}
	})
}

func TestCompactEndingOnToolOutput(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, geminiCompactionReply)
	}
	cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
	body := `{"model":"gemini-2.5-pro","input":[{"type":"message","role":"user","content":"List the files."},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"}],` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]}`
	var rec recorder
	if err := compactTo(context.Background(), compactCall(settingsFor(srv, kindGemini), cred, "gemini-2.5-pro", body), &rec); err != nil {
		t.Fatalf("compactTo failed: %v", err)
	}
	var sent struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse json.RawMessage `json:"functionResponse"`
				Text             string          `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	raw := srv.Requests()[0].Body
	if err := json.Unmarshal(raw, &sent); err != nil || len(sent.Contents) != 3 {
		t.Fatalf("contents = %s (%v), want 3 turns", raw, err)
	}
	last := sent.Contents[2]
	if last.Role != "user" || len(last.Parts) != 2 || last.Parts[0].FunctionResponse == nil || !strings.Contains(last.Parts[1].Text, "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("last turn = %+v, want user turn holding functionResponse and compaction prompt", last)
	}
	if sent.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
		t.Errorf("toolConfig = %+v, want mode NONE", sent.ToolConfig)
	}
}

func TestCompactWithoutSummary(t *testing.T) {
	cases := []struct {
		name, reply, want string
	}{
		{"max_tokens empty", `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MAX_TOKENS","index":0}]}`, "MAX_TOKENS"},
		{"max_tokens partial", `{"candidates":[{"content":{"role":"model","parts":[{"text":"cut off"}]},"finishReason":"MAX_TOKENS","index":0}]}`, "MAX_TOKENS"},
		{"safety finish", `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY","index":0}]}`, "SAFETY"},
		{"prompt blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, "SAFETY"},
		{"thought only on stop", `{"candidates":[{"content":{"role":"model","parts":[{"text":"only thought","thought":true}]},"finishReason":"STOP","index":0}]}`, "STOP"},
		{"tool call on stop", `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"shell","args":{}}}]},"finishReason":"STOP","index":0}]}`, "STOP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := geminitest.New(t)
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			var rec recorder
			err := compactTo(context.Background(), compactCall(settingsFor(srv, kindGemini), cred, "gemini-2.5-pro", compactSampleBody), &rec)
			var e *llm.Error
			if !errors.As(err, &e) || e.Retryable || e.Scope != llm.ScopeRequest || !strings.Contains(e.Message, tc.want) {
				t.Errorf("err = %+v; want non-retryable ScopeRequest naming %q", err, tc.want)
			}
			if e.Usage != nil {
				t.Errorf("err.Usage = %+v, want nil when reply has no usageMetadata", e.Usage)
			}
		})
	}
}

func TestCompactWithoutSummaryKeepsUsage(t *testing.T) {
	const fullUsage = `"usageMetadata":{"promptTokenCount":144,"cachedContentTokenCount":20,"candidatesTokenCount":76,"thoughtsTokenCount":200}`
	cases := []struct {
		name      string
		reply     string
		wantIn    int64
		wantOut   int64
		wantCache int64
		wantThink int64
		hasOut    bool
	}{
		{
			name:      "max_tokens partial",
			reply:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"cut off"}]},"finishReason":"MAX_TOKENS","index":0}],` + fullUsage + `}`,
			wantIn:    144,
			wantOut:   276,
			wantCache: 20,
			wantThink: 200,
			hasOut:    true,
		},
		{
			name:      "safety finish",
			reply:     `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY","index":0}],` + fullUsage + `}`,
			wantIn:    144,
			wantOut:   276,
			wantCache: 20,
			wantThink: 200,
			hasOut:    true,
		},
		{
			name:      "thought only on stop",
			reply:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"only thought","thought":true}]},"finishReason":"STOP","index":0}],` + fullUsage + `}`,
			wantIn:    144,
			wantOut:   276,
			wantCache: 20,
			wantThink: 200,
			hasOut:    true,
		},
		{
			name:   "prompt blocked",
			reply:  `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":144}}`,
			wantIn: 144,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := geminitest.New(t)
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.reply)
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			var rec recorder
			err := compactTo(context.Background(), compactCall(settingsFor(srv, kindGemini), cred, "gemini-2.5-pro", compactSampleBody), &rec)
			var e *llm.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *llm.Error", err)
			}
			if e.Usage == nil || e.Usage.InputTokens == nil || *e.Usage.InputTokens != tc.wantIn {
				t.Fatalf("err.Usage = %+v, want input %d", e.Usage, tc.wantIn)
			}
			if tc.hasOut {
				if e.Usage.OutputTokens == nil || *e.Usage.OutputTokens != tc.wantOut ||
					e.Usage.CachedInputTokens == nil || *e.Usage.CachedInputTokens != tc.wantCache ||
					e.Usage.ReasoningTokens == nil || *e.Usage.ReasoningTokens != tc.wantThink {
					t.Errorf("err.Usage = %+v; want %d in (%d cached), %d out (%d reasoning)", e.Usage, tc.wantIn, tc.wantCache, tc.wantOut, tc.wantThink)
				}
			}
		})
	}
}

func TestCompactUpstreamErrorsAndRedaction(t *testing.T) {
	t.Run("rate limit maps to a retryable model-scope error", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			geminitest.RateLimited(w, 7)
		}
		cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
		var rec recorder
		err := compactTo(context.Background(), compactCall(settingsFor(srv, kindGemini), cred, "gemini-2.5-pro", compactSampleBody), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.RetryAfterMS != 7000 {
			t.Errorf("err = %+v; want retryable CodeRateLimit on ScopeModel with 7000ms", err)
		}
	})

	t.Run("vertex token redacted from upstream error", func(t *testing.T) {
		srv := geminitest.New(t)
		tokens = &tokenCache{entries: map[string]*tokenEntry{}}
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"code":500,"message":"bad token `+tok+`","status":"INTERNAL"}}`)
		}
		cred := &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("compact-proj", true)}
		var rec recorder
		err := compactTo(context.Background(), compactCall(settingsFor(srv, kindVertexSA), cred, "gemini-2.5-pro", compactSampleBody), &rec)
		var e *llm.Error
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want *llm.Error", err)
		}
		if strings.Contains(e.Message, "geminitest-token-") || strings.Contains(string(e.Raw), "geminitest-token-") {
			t.Errorf("vertex access token leaked in error: %+v", e)
		}
	})

	t.Run("invalid request body never reaches upstream", func(t *testing.T) {
		srv := geminitest.New(t)
		cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
		var rec recorder
		err := compactTo(context.Background(), compactCall(settingsFor(srv, kindGemini), cred, "gemini-2.5-pro", `{"model":`), &rec)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeRequest {
			t.Errorf("err = %+v, want invalid_request on ScopeRequest", err)
		}
		if len(srv.Requests()) != 0 {
			t.Errorf("upstream got %d requests, want 0", len(srv.Requests()))
		}
	})
}

func TestCompactionHelpers(t *testing.T) {
	req, err := compactionRequest(llm.Request{
		Model:      "gemini-2.5-pro",
		DropPolicy: llm.DropReject,
		Caller:     "c-1",
		Options:    llm.Options{ThinkingSuffix: "medium"},
		Native:     &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(compactSampleBody), Headers: map[string]string{"x-test": "1"}},
	})
	if err != nil {
		t.Fatalf("compactionRequest: %v", err)
	}
	if req.Stream || req.Model != "gemini-2.5-pro" || req.Options.Stop != nil || req.Options.ResponseFormat != nil ||
		req.Options.ParallelToolCalls != nil || req.Options.ToolChoice == nil || req.Options.ToolChoice.Mode != llm.ToolChoiceNone ||
		req.Options.ThinkingSuffix != "medium" || req.DropPolicy != llm.DropReject || req.Caller != "c-1" {
		t.Errorf("unexpected normalized request: %+v", req.Options)
	}
	last := req.Items[len(req.Items)-1]
	if last.Role != llm.RoleUser || len(last.Content) != 1 || last.Content[0].Text != codexSummarizationPrompt {
		t.Errorf("last item = %+v, want user codexSummarizationPrompt", last)
	}

	reply, _, err := compactionReply([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`), "gemini-2.5-pro", 1700000000)
	if err != nil {
		t.Fatalf("compactionReply: %v", err)
	}
	if !strings.Contains(string(reply), `"id":"resp_1700000000"`) {
		t.Errorf("reply = %s, want fallback id resp_1700000000", reply)
	}

	_, _, err = compactionReply([]byte(`not json`), "gemini-2.5-pro", 1700000000)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
		t.Errorf("malformed reply err = %+v, want CodeUpstream", err)
	}
}
