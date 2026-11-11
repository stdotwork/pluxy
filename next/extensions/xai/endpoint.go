package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The CLI chat proxy has no compaction endpoint, even for a Grok login.
func compactBaseURL(base string) string {
	if strings.TrimRight(base, "/") == cliChatBaseURL {
		return defaultBaseURL
	}
	return base
}

func endpoint(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	req := call.Request
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIResponsesCompact {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "the xAI extension serves only Responses compaction endpoint calls"}
	}
	if req.Stream {
		return &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest, Message: "compaction does not support streaming"}
	}
	var inputObject map[string]json.RawMessage
	if json.Unmarshal(req.Native.Body, &inputObject) != nil || inputObject == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest, Message: "request body is not a JSON object"}
	}
	if string(inputObject["stream"]) == "true" {
		return &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest, Message: "compaction does not support streaming"}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	tg, err := accountTarget(s, call.Account.Credential)
	if err != nil {
		return err
	}
	// Apply the usual xAI shaping, then the compact-specific removals.
	native := *req.Native
	native.Format = llm.FormatOpenAIResponses
	req.Native = &native
	body, _, err := buildBody(req)
	if err != nil {
		return err
	}
	m, err := decodeObject(body)
	if err != nil {
		return err
	}
	for _, k := range []string{"stream", "tools", "tool_choice", "max_output_tokens", "temperature", "top_p", "top_k", "stop"} {
		delete(m, k)
	}
	var original struct {
		Previous string          `json:"previous_response_id"`
		Store    json.RawMessage `json:"store"`
		Include  json.RawMessage `json:"include"`
	}
	_ = json.Unmarshal(native.Body, &original)
	// Compaction does not opt into storage or encrypted reasoning by default.
	if len(original.Store) == 0 {
		delete(m, "store")
	}
	if len(original.Include) == 0 {
		delete(m, "include")
	} else {
		var include any
		_ = json.Unmarshal(original.Include, &include)
		m["include"] = include
	}
	if previous := strings.TrimSpace(original.Previous); previous != "" {
		m["previous_response_id"] = previous
	}
	if input, ok := m["input"].([]any); ok {
		kept := input[:0]
		for _, v := range input {
			item, _ := v.(map[string]any)
			if item["type"] != "compaction_trigger" {
				kept = append(kept, v)
			}
		}
		m["input"] = kept
	}
	body, err = marshal(m)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, compactBaseURL(s.BaseURL)+"/responses/compact", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeCredential, Message: "bad base_url: " + err.Error()}
	}
	hreq.Header.Set("Authorization", "Bearer "+tg.token)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", "pluxy-xai/"+version)
	if conv := promptCacheKey(body); conv != "" {
		hreq.Header.Set("x-grok-conv-id", conv)
	}
	resp, err := client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var op *net.OpError
		return &llm.Error{Code: llm.CodeUnavailable, Scope: llm.ScopeEndpoint, Retryable: errors.As(err, &op) && op.Op == "dial", Message: "upstream unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := upstreamError(resp, req.Model)
		// An absent endpoint says nothing about generation on this account or model.
		if e.Code == llm.CodeNotFound && !e.Retryable {
			e.Scope = llm.ScopeRequest
		}
		return e
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "reading compaction reply: " + err.Error()}
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	if len(reply) > protocol.MaxFrame || json.Unmarshal(reply, &r) != nil || !bytes.HasPrefix(bytes.TrimSpace(reply), []byte("{")) {
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "the upstream compaction reply is not a JSON object within the size limit"}
	}
	var usage *llm.Usage
	if len(r.Usage) > 0 && string(r.Usage) != "null" {
		var u struct {
			Input        *int64 `json:"input_tokens"`
			Output       *int64 `json:"output_tokens"`
			InputDetails struct {
				Cached *int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				Reasoning *int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		}
		if json.Unmarshal(r.Usage, &u) == nil {
			usage = &llm.Usage{InputTokens: u.Input, OutputTokens: u.Output, CachedInputTokens: u.InputDetails.Cached, ReasoningTokens: u.OutputDetails.Reasoning, Raw: r.Usage}
		}
	}
	if err := out.StartBody(llm.FormatOpenAIResponsesCompact, "application/json"); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, usage)
}
