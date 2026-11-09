package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// maxEndpointReply limits what is read of an upstream's whole (not streamed) endpoint reply.
const maxEndpointReply = 64 << 20

// endpoint serves endpoint calls: requests to Codex endpoints beyond generation.
func endpoint(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	switch format {
	case llm.FormatOpenAIResponsesCompact:
		return compact(ctx, call, out)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("the Codex extension does not serve %q", format)}
}

// compact serves Responses compaction: the client's body goes to {base_url}/responses/compact
// as today's core sends it, nearly as it came (the stream flag removed, instructions given, the
// model renamed and a thinking suffix's effort applied), and the upstream's JSON reply comes back
// as it is. The Codex CLI calls it to shorten a long conversation; the reply's compaction item
// goes back in the next request's input.
func compact(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	req := call.Request
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Native.Body, &m); err != nil || m == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = raw(req.Model)
	delete(m, "stream")
	if v, ok := m["instructions"]; !ok || string(v) == "null" {
		m["instructions"] = raw("")
	}
	if sfx := req.Options.ThinkingSuffix; sfx != "" {
		var r map[string]json.RawMessage
		_ = json.Unmarshal(m["reasoning"], &r)
		if r == nil {
			r = map[string]json.RawMessage{}
		}
		r["effort"] = raw(llm.ThinkingEffort(sfx))
		m["reasoning"] = raw(r)
	}
	// The prompt cache key, as on /responses: the client's, or one derived from the
	// conversation's start when the client names no session.
	sh := shaper{m: map[string]json.RawMessage{"prompt_cache_key": m["prompt_cache_key"], "input": m["input"]}, req: req,
		model: req.Model, accountID: call.Account.ID}
	if err := sh.input(); err != nil {
		return err
	}
	sh.promptCacheKey()
	if k, ok := sh.m["prompt_cache_key"]; ok {
		m["prompt_cache_key"] = k
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ur := &upstreamRequest{body: body, model: req.Model, sessionID: sh.sessionID}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+"/responses/compact", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(hreq.Header, req, ur, s, a)
	hreq.Header.Set("Accept", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return endpointStatusError(resp, req.Model)
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxEndpointReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if len(reply) > maxEndpointReply {
		return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream's compaction reply is larger than %d MiB", maxEndpointReply>>20), Scope: llm.ScopeEndpoint}
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(reply, &r) != nil {
		return &llm.Error{Code: llm.CodeUpstream, Message: "the upstream's compaction reply is not JSON", Scope: llm.ScopeEndpoint}
	}
	if err := out.StartBody(llm.FormatOpenAIResponsesCompact, "application/json"); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, responseUsage(r.Usage))
}

// endpointStatusError is statusError for an endpoint call. A 404 that does not name a missing
// model means the upstream has no such endpoint; that says nothing about the model's generation on
// the account, which a model-scoped error would block, so it is scoped to the request.
func endpointStatusError(resp *http.Response, model string) *llm.Error {
	e := statusError(resp, model)
	if e.Code == llm.CodeNotFound && !e.Retryable {
		e.Scope = llm.ScopeRequest
	}
	return e
}

// baseURL returns where the account's requests go, without a trailing slash.
func baseURL(s settings, a account) string {
	base := s.BaseURL
	if base == "" {
		base = defaultAPIBaseURL
		if a.subscription {
			base = defaultSubscriptionBaseURL
		}
	}
	return strings.TrimRight(base, "/")
}

// requestFailed turns a failed upstream request into a structured error.
func requestFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var op *net.OpError
	dial := errors.As(err, &op) && op.Op == "dial"
	// A failed dial never reached the upstream, so trying again is safe.
	return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
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
	return &llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: u.InputTokensDetails.CachedTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens, Raw: raw}
}
