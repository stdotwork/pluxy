package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// tokenCountingBeta is the beta Anthropic's SDKs send with a token count, which today's core adds
// to every count it sends Kimi.
const tokenCountingBeta = "token-counting-2024-11-01"

// countTokens counts a request's input tokens with Kimi's Messages count endpoint,
// {base_url}/v1/messages/count_tokens, as today's core does for every client format: a Messages
// request is counted as Kimi's Messages API would get it, any other is rebuilt as Messages with
// the fields count_tokens takes.
func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	token, _, err := bearer(call.Account.Credential)
	if err != nil {
		return err
	}
	body, native, err := countBody(call.Request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/v1/messages/count_tokens?beta=true", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if native {
		for _, h := range slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders) {
			if v := call.Request.Native.Headers[h]; v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	req.Header.Set("Anthropic-Beta", withBeta(req.Header.Get("Anthropic-Beta"), tokenCountingBeta))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return upstreamError(resp, call.Request.Model)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	n, err := anthropicmessages.DecodeCount(raw)
	if err != nil {
		return err
	}
	if err := out.Start(llm.ResponseInfo{Model: call.Request.Model, Format: llm.FormatAnthropicMessages}); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: &n, Raw: llm.RawJSON(raw)})
}

// countBody returns the count_tokens body for req, and whether it is the client's own Messages
// body. Either way the model is Kimi's name for it and Claude Code's billing block is left out of
// the system prompt, as for generating.
func countBody(req llm.Request) ([]byte, bool, error) {
	native := req.Native != nil && req.Native.Format == llm.FormatAnthropicMessages
	var body []byte
	if native {
		body = req.Native.Body
	} else {
		b, _, err := anthropicmessages.EncodeRequest(req)
		if err != nil {
			return nil, false, err
		}
		if body, err = anthropicmessages.CountRequest(b); err != nil {
			return nil, false, err
		}
	}
	m, err := decodeObject(body)
	if err != nil || m == nil {
		return nil, false, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = upstreamModel(req.Model)
	stripBillingBlock(m)
	out, err := marshal(m)
	return out, native, err
}

// withBeta adds beta to a comma-separated anthropic-beta value unless it is already there.
func withBeta(v, beta string) string {
	for _, b := range strings.Split(v, ",") {
		if strings.TrimSpace(b) == beta {
			return v
		}
	}
	if strings.TrimSpace(v) == "" {
		return beta
	}
	return v + "," + beta
}
