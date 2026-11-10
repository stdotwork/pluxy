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
	"github.com/stdotwork/pluxy/next/protocol"
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
	if isSIWC(call.Account.Credential) {
		// A SIWC account serves compaction only: today's core makes its images through
		// Responses instead, which this extension does not, and sends its Alpha Search with the
		// ChatGPT sign-in's own token, which a SIWC account does not have.
		switch {
		case format != llm.FormatOpenAIResponsesCompact:
			return siwcUnsupported(fmt.Sprintf("%q", format))
		case codexClient(call.Request):
			return siwcCodexClient()
		}
	}
	switch format {
	case llm.FormatOpenAIResponsesCompact:
		return compact(ctx, call, out)
	case llm.FormatCodexAlphaSearch:
		return alphaSearch(ctx, call, out)
	case llm.FormatOpenAIImagesGenerations:
		return images(ctx, call, out, "/images/generations")
	case llm.FormatOpenAIImagesEdits:
		return images(ctx, call, out, "/images/edits")
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("the Codex extension does not serve %q", format)}
}

// compact serves Responses compaction: the client's body goes to {base_url}/responses/compact
// as today's core sends it, nearly as it came (the stream flag removed, instructions given unless
// the request is Responses-lite, the model renamed and a thinking suffix's effort applied), and the
// upstream's JSON reply comes back as it is. The Codex CLI calls it to shorten a long conversation; the reply's compaction item
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
	if a.siwc {
		// Today's core leaves store to the OpenAI API's default on a SIWC compaction.
		delete(m, "store")
	}
	// A Responses-lite request goes without instructions when it has none, as on /responses.
	if v, ok := m["instructions"]; (!ok || string(v) == "null") && !liteRequest(req.Native.Headers, m) {
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
	if a.siwc {
		siwcBody(m, req.Options.ThinkingSuffix != "")
	}
	// Replayed web searches need a web search tool, as on /responses. Lite is read from the whole
	// body, since client_metadata may say it.
	webSearchForHistory(m, liteRequest(req.Native.Headers, m))
	// The endpoint rejects the same tool schema forms as /responses; today's core rewrites them
	// in compaction requests too.
	var tools []json.RawMessage
	if json.Unmarshal(m["tools"], &tools) == nil && fixToolSchemas(tools) {
		m["tools"] = raw(tools)
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
	reply, err := readReply(ctx, resp, "compaction")
	if err != nil {
		return err
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

// alphaSearch serves Codex's search endpoint: the client's body goes to {base_url}/alpha/search
// as today's core sends it, nearly as it came, and the upstream's reply comes back whole. A
// subscription always serves it; an API key only when its account sets alpha_search, since not
// every upstream has the endpoint, and names its base_url.
func alphaSearch(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	if !a.subscription && !s.AlphaSearch {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this API key does not serve Alpha Search (the account's alpha_search setting is off)"}
	}
	if !a.subscription && strings.TrimSpace(s.BaseURL) == "" {
		// Today's core keeps no Codex API key without a base URL, so such a key never serves
		// search there; the search is not sent to the default API host, which has no such
		// endpoint, and core tries another account.
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this API key does not serve Alpha Search (the account has no base_url)"}
	}
	req := call.Request
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Native.Body, &m); err != nil || m == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	// The search endpoint takes no prompt cache hints; today's core removes them too.
	delete(m, "prompt_cache_key")
	delete(m, "prompt_cache_retention")
	if _, ok := m["model"]; ok && req.Model != "" {
		// The account may know the model by another name (an alias or prefix). Routing renames
		// the body's model for every endpoint call; today's core does so only for an API key.
		m["model"] = raw(req.Model)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+"/alpha/search", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	h := hreq.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+a.token)
	h.Set("Originator", alphaSearchOriginator)
	for _, name := range alphaSearchHeaders {
		if v := req.Native.Headers[name]; v != "" {
			h.Set(name, v)
		}
	}
	if a.subscription && a.accountID != "" {
		h.Set("Chatgpt-Account-Id", a.accountID)
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return endpointStatusError(resp, req.Model)
	}
	reply, err := readReply(ctx, resp, "search")
	if err != nil {
		return err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if err := out.StartBody(llm.FormatCodexAlphaSearch, contentType); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

// alphaSearchOriginator is the originator header of a search request: the Codex CLI's own name,
// which today's core sends here whatever the client's.
const alphaSearchOriginator = "codex_cli_rs"

// alphaSearchHeaders are the client's request headers a search request carries on (lower-case,
// as the request's native headers name them), the ones today's core passes.
var alphaSearchHeaders = []string{"version", "user-agent", "session_id", "x-client-request-id"}

// readReply reads an upstream's whole endpoint reply; what names the reply in an error message.
func readReply(ctx context.Context, resp *http.Response, what string) ([]byte, error) {
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxEndpointReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if len(reply) > maxEndpointReply {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream's %s reply is larger than %d MiB", what, maxEndpointReply>>20), Scope: llm.ScopeEndpoint}
	}
	return reply, nil
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

// imageModels are the image models the Codex upstream serves on its Images API, listed for every
// account as today's core lists them (its Codex built-ins), with the names and dates it gives.
var imageModels = []protocol.Model{
	{ID: "gpt-image-1.5", DisplayName: "GPT Image 1.5", Version: "gpt-image-1.5", Created: 1704067200},
	{ID: "gpt-image-2", DisplayName: "GPT Image 2", Version: "gpt-image-2", Created: 1704067200},
	{ID: "gpt-image-2.5-flare", DisplayName: "GPT Image 2.5 Flare", Version: "gpt-image-2.5-flare", Created: 1704067200},
	{ID: "gpt-image-2.5-sunburst", DisplayName: "GPT Image 2.5 Sunburst", Version: "gpt-image-2.5-sunburst", Created: 1704067200},
	{ID: "gpt-image-2.5", DisplayName: "GPT Image 2.5", Version: "gpt-image-2.5", Created: 1704067200},
}

// imageModel returns the image model a request names, as the upstream knows it (any letter
// case), or "" for a model that is not one.
func imageModel(name string) string {
	for _, m := range imageModels {
		if strings.EqualFold(name, m.ID) {
			return m.ID
		}
	}
	return ""
}

// images serves the Images API (generations and edits) through the account's Codex upstream:
// the client's JSON body goes to {base_url}{path}, the model named as the upstream knows it and
// stream left out unless it is true, and the upstream's reply (JSON, or server-sent events for a
// stream) comes back as it is, as today's core sends both. A multipart edit arrives in its JSON
// form (core reads it), as today's core also sends it. A free ChatGPT plan, and an account whose
// "images" setting is false, do not serve it.
func images(ctx context.Context, call sdk.Call, out *sdk.Stream, path string) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	if !s.imagesAPI() {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "the account's \"images\" setting turns the Images API off"}
	}
	if a.subscription && strings.EqualFold(strings.TrimSpace(a.planType), "free") {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "a free ChatGPT plan cannot use the Images API"}
	}
	req := call.Request
	model := imageModel(req.Model)
	if model == "" {
		names := make([]string, len(imageModels))
		for i, m := range imageModels {
			names[i] = m.ID
		}
		return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
			Message: fmt.Sprintf("Model %s is not supported on /v1/images/generations or /v1/images/edits. Use %s.", req.Model, strings.Join(names, ", "))}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Native.Body, &m); err != nil || m == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = raw(model)
	stream := string(m["stream"]) == "true"
	if !stream {
		delete(m, "stream")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+path, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	h := hreq.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if stream {
		h.Set("Accept", "text/event-stream")
	}
	h.Set("Authorization", "Bearer "+a.token)
	sent := req.Native.Headers
	for _, name := range passedHeaders {
		if v := sent[name]; v != "" {
			h.Set(name, v)
		}
	}
	setIdentity(h, sent, s, a)
	resp, err := client.Do(hreq)
	if err != nil {
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return endpointStatusError(resp, model)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
		if stream {
			ct = "text/event-stream"
		}
	}
	if err := out.StartBody(call.Request.Native.Format, ct); err != nil {
		return err
	}
	// A stream goes on piece by piece as it arrives; a JSON reply is kept too, for its usage.
	var kept bytes.Buffer
	var dst io.Writer = out.BodyWriter()
	bw := dst.(*sdk.BodyWriter)
	if !strings.HasPrefix(ct, "text/event-stream") {
		dst = io.MultiWriter(bw, &limitedBuffer{b: &kept, max: maxEndpointReply})
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if err := bw.Close(); err != nil {
		return err
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(kept.Bytes(), &r)
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, responseUsage(r.Usage))
}

// limitedBuffer keeps at most max bytes of what is written to it and drops the rest, never
// failing a write.
type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
