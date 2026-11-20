package main

import (
	"context"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// inputTokens counts a Responses request's input tokens. A Responses upstream takes the same
// request at {base_url}/responses/input_tokens and returns the count; the other wires have no
// such endpoint, so core estimates the count with another account.
func inputTokens(ctx context.Context, call sdk.Call, out bodySender) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	if s.WireAPI != wireResponses {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's upstream speaks " + formatNames[wire.format] + ", which has no Responses input token count endpoint"}
	}
	call.Request.Stream = false
	resp, err := postBody(ctx, call, s, wire, key, "/responses/input_tokens", call.Request.Native.Body)
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
