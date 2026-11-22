package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// codexSummarizationPrompt is the handoff summary instruction the Codex CLI sends when
// compacting locally (codex-rs/core/templates/compact/prompt.md).
const codexSummarizationPrompt = "You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.\n\nInclude:\n- Current progress and key decisions made\n- Important context, constraints, or user preferences\n- What remains to be done (clear next steps)\n- Any critical data, examples, or references needed to continue\n\nBe concise, structured, and focused on helping the next LLM seamlessly continue the work."

// codexSummaryPrefix starts the user message that holds a compaction summary in the Codex CLI's
// conversation (codex-rs/core/templates/compact/summary_prefix.md).
const codexSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

type bodySender interface {
	StartBody(format, contentType string) error
	Body(chunk string) error
	Finish(f llm.Finish, usage *llm.Usage) error
}

func compact(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return compactTo(ctx, call, out)
}

// compactTo serves Responses compaction (POST /v1/responses/compact and
// /backend-api/codex/responses/compact) on an OpenAI-compatible account by its wire_api:
//   - "responses": passes the client's body through to POST {base_url}/responses/compact
//     unchanged and returns the upstream's reply as it came;
//   - "chat": translates the Responses conversation to one non-streamed Chat Completions request
//     with tool_choice "none", structured output and stop sequences cleared, and the Codex CLI's
//     compaction prompt appended as a last user message, and returns OpenAI's response.compaction
//     JSON object;
//   - "messages" and "gemini": answers unsupported with scope credential so core tries another
//     account.
func compactTo(ctx context.Context, call sdk.Call, out bodySender) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	if format != llm.FormatOpenAIResponsesCompact {
		return &llm.Error{
			Code:      llm.CodeUnsupported,
			Scope:     llm.ScopeCredential,
			Retryable: true,
			Message:   fmt.Sprintf("the OpenAI-compatible extension does not serve %q", format),
		}
	}
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	switch s.WireAPI {
	case wireResponses:
		return compactResponses(ctx, call, s, wire, key, out)
	case wireChat:
		return compactChat(ctx, call, s, wire, key, out)
	default:
		return &llm.Error{
			Code:      llm.CodeUnsupported,
			Scope:     llm.ScopeCredential,
			Retryable: true,
			Message:   "this account's upstream speaks " + formatNames[wire.format] + ", which does not serve Responses compaction",
		}
	}
}

func compactResponses(ctx context.Context, call sdk.Call, s settings, wire wireInfo, key string, out bodySender) error {
	call.Request.Stream = false
	resp, err := postBody(ctx, call, s, wire, key, "/responses/compact", call.Request.Native.Body)
	if err != nil {
		return noEndpoint(err, "compaction", "/responses/compact")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &r)
	if err := out.StartBody(llm.FormatOpenAIResponsesCompact, "application/json"); err != nil {
		return err
	}
	if err := out.Body(string(raw)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, responseUsage(r.Usage))
}

// noEndpoint scopes to the request a 404 from a side endpoint, {base_url} plus path, that does not
// say the model is unknown, as the Codex and xAI extensions do for their endpoints. Many vendors
// have no such path, and that says nothing about the model's generation on the account, which a
// model-scoped error would block for hours. The message names the endpoint (name) and the path the
// request went to. A 404 that says the model does not exist is left as it is, and so is any other
// error.
func noEndpoint(err error, name, path string) error {
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Retryable {
		return err
	}
	e.Scope = llm.ScopeRequest
	e.Message = fmt.Sprintf("this account's upstream has no %s endpoint (POST %s answered 404)", name, path)
	return e
}

// noCountEndpoint answers a 404 from a token count path that does not say the model is unknown as
// an account that cannot count (unsupported, on the credential), so core answers with its estimate,
// or with this error when token_count_estimate is off. It stays non-retryable, so that error keeps
// its 404 and names the endpoint (name) and the path. A 404 that says the model does not exist is left as it is, and so is
// any other error.
func noCountEndpoint(err error, name, path string) error {
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeNotFound || e.Retryable {
		return err
	}
	e.Code, e.Scope = llm.CodeUnsupported, llm.ScopeCredential
	e.Message = fmt.Sprintf("this account's upstream has no %s endpoint (POST %s answered 404)", name, path)
	return e
}

func compactChat(ctx context.Context, call sdk.Call, s settings, wire wireInfo, key string, out bodySender) error {
	req, err := compactionRequest(call.Request)
	if err != nil {
		return err
	}
	// The compaction request is shaped for the vendor like any Chat request (shaping.go).
	body, _, err := upstreamBody(req, wire.format, s)
	if err != nil {
		return err
	}
	call.Request = req
	resp, err := postBody(ctx, call, s, wire, key, endpoint(s.WireAPI, req), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	reply, usage, err := compactionReply(raw, req.Model, time.Now().Unix())
	if err != nil {
		return err
	}
	if err := out.StartBody(llm.FormatOpenAIResponsesCompact, "application/json"); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

// compactionRequest reads the client's Responses body as a non-streamed Chat Completions request
// for the call's model, clears options incompatible with a prose summary (stop sequences and
// structured output), sets tool_choice to none so the model answers in text while keeping tool
// declarations for any tool history, and appends the Codex compaction prompt.
func compactionRequest(call llm.Request) (llm.Request, error) {
	req, err := openairesponses.ParseRequest(call.Native.Body)
	if err != nil {
		return llm.Request{}, err
	}
	if call.Model != "" {
		req.Model = call.Model
	}
	req.Stream = false
	req.Native.Headers = call.Native.Headers
	req.Options.ThinkingSuffix = call.Options.ThinkingSuffix
	req.DropPolicy, req.Caller = call.DropPolicy, call.Caller
	req.Options.Stop, req.Options.ResponseFormat = nil, nil
	req.Options.ParallelToolCalls = nil
	req.Options.ToolChoice = &llm.ToolChoice{Mode: llm.ToolChoiceNone}
	req.Items = append(req.Items, llm.Item{
		Type:        llm.ItemTypeMessage,
		Role:        llm.RoleUser,
		TextContent: true,
		Content:     []llm.Part{{Type: llm.PartText, Text: codexSummarizationPrompt}},
	})
	return req, nil
}

// compactionReply turns a non-streamed Chat Completions response into OpenAI's response.compaction
// JSON object for model, created at the given Unix time, and its usage. Reasoning content is
// excluded from the summary; an incomplete or empty summary is reported as an error on this
// request.
func compactionReply(raw []byte, model string, created int64) ([]byte, *llm.Usage, error) {
	events, err := openaichat.DecodeResponse(raw)
	if err != nil {
		return nil, nil, err
	}
	var (
		responseID string
		summary    strings.Builder
		toolCall   bool
		finish     *llm.Finish
		usage      *llm.Usage
	)
	for _, ev := range events {
		switch ev.Type {
		case llm.EventResponseStart:
			if ev.Response != nil {
				responseID = ev.Response.ID
			}
		case llm.EventItemStart:
			if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
				toolCall = true
			}
		case llm.EventTextDelta:
			summary.WriteString(ev.Delta)
		case llm.EventFinish:
			finish = ev.Finish
			usage = ev.Usage
		}
	}
	text := summary.String()
	badFinish := finish != nil && finish.Reason != llm.FinishStop && !(finish.Reason == llm.FinishOther && finish.RawReason == "")
	if badFinish || toolCall || strings.TrimSpace(text) == "" {
		reason := ""
		if finish != nil {
			reason = finish.RawReason
			if reason == "" {
				reason = finish.Reason
			}
		}
		return nil, nil, &llm.Error{
			Code:    llm.CodeUpstream,
			Status:  http.StatusBadGateway,
			Scope:   llm.ScopeRequest,
			Message: fmt.Sprintf("Chat Completions wrote no compaction summary (finish_reason %q)", reason),
			Usage:   usage,
		}
	}
	id := "resp_" + strings.TrimPrefix(responseID, "resp_")
	if responseID == "" {
		id = fmt.Sprintf("resp_%d", created)
	}
	var in, cached, out, reasoning int64
	if usage != nil {
		if usage.InputTokens != nil {
			in = *usage.InputTokens
		}
		if usage.CachedInputTokens != nil {
			cached = *usage.CachedInputTokens
		}
		if usage.OutputTokens != nil {
			out = *usage.OutputTokens
		}
		if usage.ReasoningTokens != nil {
			reasoning = *usage.ReasoningTokens
		}
	}
	reply := map[string]any{
		"id":         id,
		"object":     "response.compaction",
		"created_at": created,
		"model":      model,
		"output": []any{map[string]any{
			"type": "message", "role": "user", "status": "completed",
			"content": []any{map[string]any{"type": "input_text", "text": codexSummaryPrefix + "\n" + text}},
		}},
		"usage": map[string]any{
			"input_tokens":          in,
			"input_tokens_details":  map[string]int64{"cached_tokens": cached},
			"output_tokens":         out,
			"output_tokens_details": map[string]int64{"reasoning_tokens": reasoning},
			"total_tokens":          in + out,
		},
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return nil, nil, err
	}
	return body, usage, nil
}

// responseUsage reads a Responses usage object; nil when there is none.
func responseUsage(raw json.RawMessage) *llm.Usage {
	var u struct {
		InputTokens        *int64 `json:"input_tokens"`
		OutputTokens       *int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &u) != nil {
		return nil
	}
	return &llm.Usage{
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.InputTokensDetails.CachedTokens,
		ReasoningTokens:   u.OutputTokensDetails.ReasoningTokens,
		Raw:               raw,
	}
}
