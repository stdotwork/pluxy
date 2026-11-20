package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// compactBeta is the beta Anthropic's on-demand compaction needs: a request with the compaction
// parameter is refused without it.
const compactBeta = "compact-2026-09-04"

// codexSummaryPrefix starts the user message that holds a compaction summary in the Codex CLI's
// conversation, as the Codex CLI writes it when it compacts on its own side
// (codex-rs/prompts/templates/compact/summary_prefix.md).
const codexSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

// endpoint serves endpoint calls: Responses compaction (compact). Any other endpoint is not
// served, so core tries another account.
func endpoint(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	if format == llm.FormatOpenAIResponsesCompact {
		return compact(ctx, call, out)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("the Claude extension does not serve %q", format)}
}

// compact serves Responses compaction, which the Codex CLI calls to shorten a long conversation,
// with Anthropic's on-demand compaction: the Responses request is translated to Messages as for
// /v1/responses, sent once, not streamed, with "compaction": {"type": "summarize"} and its beta,
// and Claude's summary comes back as OpenAI's compaction reply, one JSON object. Its output is
// the summary as the user message the Codex CLI keeps in place of the conversation (the shape of
// its own compaction), since a later request may go to any account: Anthropic's signed
// compaction block is not passed on.
func compact(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	req, err := compactionRequest(call.Request)
	if err != nil {
		return err
	}
	// A conversation that ends on the model's turn is summarized with that turn: Anthropic takes
	// one on a compaction request (its docs' compaction loop sends one to Claude Opus 5.5), though
	// the Messages encoder drops it as a prefill on a generation for the models that refuse one.
	body, _, err := buildBodyWith(req, s.CacheTTL, anthropicmessages.EncodeOptions{KeepModelTurn: true})
	if err != nil {
		return err
	}
	if s.RebuildMidSystemMessage {
		if body, err = moveSystemMessages(body); err != nil {
			return err
		}
	}
	if body, err = compactionBody(body); err != nil {
		return err
	}
	resp, err := post(ctx, call, s, "/v1/messages", body, false, false)
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

// compactionRequest reads the client's Responses body as a generation request for the call's
// model, not streamed, without the settings Anthropic refuses on a compaction request, which
// would do nothing there: stop sequences, structured output and a forced tool choice.
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
	if tc := req.Options.ToolChoice; tc != nil && (tc.Mode == llm.ToolChoiceRequired || tc.Mode == llm.ToolChoiceTool) {
		req.Options.ToolChoice = nil
	}
	return req, nil
}

// compactionBody makes a Messages body a compaction request: it adds the compaction parameter
// and its beta (to the body's betas list, which post sends as anthropic-beta), and takes out what
// Anthropic refuses with it: stream, stop_sequences, a tool_choice of type any or tool,
// output_config.format and context_management.
func compactionBody(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	delete(m, "stream")
	delete(m, "stop_sequences")
	delete(m, "context_management")
	var tc struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(m["tool_choice"], &tc) == nil && (tc.Type == "any" || tc.Type == "tool") {
		delete(m, "tool_choice")
	}
	var oc map[string]json.RawMessage
	if json.Unmarshal(m["output_config"], &oc) == nil && oc != nil {
		if _, ok := oc["format"]; ok {
			delete(oc, "format")
			if len(oc) == 0 {
				delete(m, "output_config")
			} else {
				m["output_config"], _ = json.Marshal(oc)
			}
		}
	}
	m["compaction"] = json.RawMessage(`{"type":"summarize"}`)
	var betas []json.RawMessage
	_ = json.Unmarshal(m["betas"], &betas)
	beta, _ := json.Marshal(compactBeta)
	m["betas"], _ = json.Marshal(append(betas, beta))
	return json.Marshal(m)
}

// compactionReply turns Anthropic's reply to a compaction request into OpenAI's compaction
// reply for model, created at the given Unix time, and its usage. A reply without a summary
// (stop_reason other than compaction, which Anthropic sends with empty content when the
// summarization call ended another way) is an error about this request.
func compactionReply(raw []byte, model string, created int64) ([]byte, *llm.Usage, error) {
	var r struct {
		ID      string `json:"id"`
		Content []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"content"`
		StopReason string          `json:"stop_reason"`
		Usage      json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Message: "the upstream's compaction reply is not JSON", Scope: llm.ScopeEndpoint}
	}
	summary := ""
	for _, b := range r.Content {
		if b.Type == "compaction" {
			summary = b.Content
		}
	}
	if r.StopReason != "compaction" || strings.TrimSpace(summary) == "" {
		return nil, nil, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest,
			Message: fmt.Sprintf("Claude wrote no compaction summary (stop_reason %q)", r.StopReason)}
	}
	in, cached, out := compactionUsage(r.Usage)
	reply := map[string]any{
		"id":         "resp_" + strings.TrimPrefix(r.ID, "msg_"),
		"object":     "response.compaction",
		"created_at": created,
		"model":      model,
		"output": []any{map[string]any{
			"type": "message", "role": "user", "status": "completed",
			"content": []any{map[string]any{"type": "input_text", "text": codexSummaryPrefix + "\n" + summary}},
		}},
		"usage": map[string]any{
			"input_tokens":          in,
			"input_tokens_details":  map[string]int64{"cached_tokens": cached},
			"output_tokens":         out,
			"output_tokens_details": map[string]int64{"reasoning_tokens": 0},
			"total_tokens":          in + out,
		},
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return nil, nil, err
	}
	u := &llm.Usage{InputTokens: &in, OutputTokens: &out, CachedInputTokens: &cached}
	if len(r.Usage) > 0 {
		u.Raw = append(json.RawMessage(nil), r.Usage...)
	}
	return body, u, nil
}

// compactionUsage reads a compaction reply's usage: the input (cache reads and writes included),
// the cache reads and the output of its iterations, where Anthropic reports the summarization
// call (the top-level counts are zero), or of the top level when it lists no iterations.
func compactionUsage(raw json.RawMessage) (in, cached, out int64) {
	type counts struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	}
	var u struct {
		counts
		Iterations []counts `json:"iterations"`
	}
	_ = json.Unmarshal(raw, &u)
	list := u.Iterations
	if len(list) == 0 {
		list = []counts{u.counts}
	}
	for _, c := range list {
		in += c.InputTokens + c.CacheReadInputTokens + c.CacheCreationInputTokens
		cached += c.CacheReadInputTokens
		out += c.OutputTokens
	}
	return in, cached, out
}
