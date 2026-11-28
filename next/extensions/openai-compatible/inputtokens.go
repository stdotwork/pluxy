package main

import (
	"context"
	"encoding/json"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func inputTokens(ctx context.Context, call sdk.Call, out bodySender) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if s.WireAPI != wireResponses {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's upstream speaks " + formatNames[wire.format] + ", which has no Responses input token count endpoint"}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Request.Native.Body, &body); err != nil || body == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the input token count request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	upstream, _, err := withModel(call.Request.Native.Body, body, call.Request.Model)
	if err != nil {
		return err
	}
	call.Request.Stream = false
	resp, err := postBody(ctx, call, s, wire, key, "/responses/input_tokens", upstream)
	if err != nil {
		return noCountEndpoint(err, "input token count", "/responses/input_tokens")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if err := out.StartBody(llm.FormatOpenAIResponsesInputTokens, contentType); err != nil {
		return err
	}
	if err := out.Body(string(raw)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}
