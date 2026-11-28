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

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const codexSummarizationPrompt = "You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.\n\nInclude:\n- Current progress and key decisions made\n- Important context, constraints, or user preferences\n- What remains to be done (clear next steps)\n- Any critical data, examples, or references needed to continue\n\nBe concise, structured, and focused on helping the next LLM seamlessly continue the work."

const codexSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

func compact(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return compactTo(ctx, call, out)
}

func compactTo(ctx context.Context, call sdk.Call, out sender) (err error) {
	ctx = withAccountID(ctx, call.Account.ID)
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	req, err := compactionRequest(call.Request)
	if err != nil {
		return err
	}
	body, tr, err := buildBodyOn(req, s)
	if err != nil {
		return err
	}
	body = cleanToolSchemas(body)
	body = fixTurns(body, s.Vertex != nil, false, !tr.Passthrough)
	t, err := resolve(ctx, s, call.Account.Credential, call.Request.Model, false)
	if err != nil {
		return err
	}
	send := &reporting{out: out, tr: tr}
	if t.token != "" {
		send.redact = protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.token})
		defer func() {
			var e *llm.Error
			if errors.As(err, &e) {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}
	if s.Vertex != nil && !vertexCallIDs(tr.From) {
		body = stripCallIDs(body)
	}
	resp, err := postBody(ctx, t, body, false, call.Request.Model)
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
	reply, usage, err := compactionReply(raw, call.Request.Model, time.Now().Unix())
	if err != nil {
		var e *llm.Error
		if errors.As(err, &e) && len(e.Raw) > 0 &&
			(e.Status == http.StatusTooManyRequests || e.Code == llm.CodeRateLimit || e.Code == llm.CodeQuota) {
			observeRateLimit(ctx, nil, e.Raw, nil, false, call.Request.Model, send.redact, time.Now())
		}
		return err
	}
	if err := send.StartBody(llm.FormatOpenAIResponsesCompact, "application/json"); err != nil {
		return err
	}
	if err := send.Body(string(reply)); err != nil {
		return err
	}
	return send.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}

func compactionRequest(call llm.Request) (llm.Request, error) {
	req, err := openairesponses.ParseRequest(call.Native.Body)
	if err != nil {
		return llm.Request{}, err
	}
	req.Model = call.Model
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

func compactionReply(raw []byte, model string, created int64) ([]byte, *llm.Usage, error) {
	events, err := geminigenerate.DecodeResponse(raw)
	if err != nil {
		return nil, nil, err
	}
	var (
		responseID string
		summary    strings.Builder
		finish     *llm.Finish
		usage      *llm.Usage
	)
	for _, ev := range events {
		switch ev.Type {
		case llm.EventResponseStart:
			if ev.Response != nil {
				responseID = ev.Response.ID
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
	if badFinish || strings.TrimSpace(text) == "" {
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
			Message: fmt.Sprintf("Gemini wrote no compaction summary (finish_reason %q)", reason),
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
