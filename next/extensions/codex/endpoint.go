package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const maxEndpointReply = 64 << 20

type inputTokensBody interface {
	StartBody(format, contentType string) error
	Body(chunk string) error
	Finish(f llm.Finish, usage *llm.Usage) error
}

func endpoint(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	format := ""
	if call.Request.Native != nil {
		format = call.Request.Native.Format
	}
	if format == "codex.models" {
		return clientModels(call, out)
	}
	if isSIWC(call.Account.Credential) {
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
	case llm.FormatOpenAIResponsesInputTokens:
		return inputTokens(ctx, call, out)
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
	lite := liteRequest(req.Native.Headers, m)
	if lite {
		m["parallel_tool_calls"] = raw(false)
	}
	if a.siwc {
		delete(m, "store")
	}
	if v, ok := m["instructions"]; (!ok || string(v) == "null") && !lite {
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
	fit := shaper{m: m, model: req.Model}
	fit.minOutputTokens()
	fit.clampEffort()
	webSearchForHistory(m, lite)
	var tools []json.RawMessage
	if json.Unmarshal(m["tools"], &tools) == nil && fixToolSchemas(tools, "tools", nil) {
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

func inputTokens(ctx context.Context, call sdk.Call, out inputTokensBody) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	if a.subscription {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "a ChatGPT subscription has no Responses input token count endpoint"}
	}
	req := call.Request
	body := []byte(req.Native.Body)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	var named string
	_ = json.Unmarshal(m["model"], &named)
	if req.Model != "" && named != req.Model {
		m["model"] = raw(req.Model)
		if body, err = json.Marshal(m); err != nil {
			return err
		}
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+"/responses/input_tokens", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	h := hreq.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
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
		return endpointStatusError(resp, req.Model)
	}
	reply, err := readReply(ctx, resp, "input token count")
	if err != nil {
		return err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if err := out.StartBody(llm.FormatOpenAIResponsesInputTokens, contentType); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

func endpointStatusError(resp *http.Response, model string) *llm.Error {
	e := statusError(resp, model)
	if e.Code == llm.CodeNotFound && !e.Retryable {
		e.Scope = llm.ScopeRequest
	}
	return e
}

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
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this API key does not serve Alpha Search (the account has no base_url)"}
	}
	req := call.Request
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Native.Body, &m); err != nil || m == nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	delete(m, "prompt_cache_key")
	delete(m, "prompt_cache_retention")
	if _, ok := m["model"]; ok && req.Model != "" {
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

const alphaSearchOriginator = "codex_cli_rs"

var alphaSearchHeaders = []string{"version", "user-agent", "session_id", "x-client-request-id"}

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

func requestFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
}

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

var imageModels = []protocol.Model{
	{ID: "gpt-image-1.5", DisplayName: "GPT Image 1.5", Version: "gpt-image-1.5", Created: 1704067200},
	{ID: "gpt-image-2", DisplayName: "GPT Image 2", Version: "gpt-image-2", Created: 1704067200},
	{ID: "gpt-image-2.5-flare", DisplayName: "GPT Image 2.5 Flare", Version: "gpt-image-2.5-flare", Created: 1704067200},
	{ID: "gpt-image-2.5-sunburst", DisplayName: "GPT Image 2.5 Sunburst", Version: "gpt-image-2.5-sunburst", Created: 1704067200},
	{ID: "gpt-image-2.5", DisplayName: "GPT Image 2.5", Version: "gpt-image-2.5", Created: 1704067200},
}

func imageModel(name string) string {
	for _, m := range imageModels {
		if strings.EqualFold(name, m.ID) {
			return m.ID
		}
	}
	return ""
}

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
	// Codex's native edits endpoint can ignore stream=true and answer with one JSON Images API
	// document. A client that asked to stream gets the events OpenAI defines for it instead.
	convert := stream && !strings.HasPrefix(ct, "text/event-stream")
	if convert {
		ct = "text/event-stream"
	}
	if err := out.StartBody(call.Request.Native.Format, ct); err != nil {
		return err
	}
	var kept bytes.Buffer
	var dst io.Writer = out.BodyWriter()
	bw := dst.(*sdk.BodyWriter)
	if convert {
		dst = &limitedBuffer{b: &kept, max: maxEndpointReply}
	} else if !strings.HasPrefix(ct, "text/event-stream") {
		dst = io.MultiWriter(bw, &limitedBuffer{b: &kept, max: maxEndpointReply})
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	if convert {
		events, err := imageStreamEvents(path, kept.Bytes())
		if err != nil {
			return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream image reply: " + err.Error(), Scope: llm.ScopeEndpoint}
		}
		if _, err := io.WriteString(bw, events); err != nil {
			return err
		}
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

func imageStreamEvents(path string, raw []byte) (string, error) {
	var r struct {
		Created      int64           `json:"created"`
		Background   string          `json:"background"`
		OutputFormat string          `json:"output_format"`
		Quality      string          `json:"quality"`
		Size         string          `json:"size"`
		Usage        json.RawMessage `json:"usage"`
		Data         []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	kind := "image_generation"
	if path == "/images/edits" {
		kind = "image_edit"
	}
	var b strings.Builder
	for _, image := range r.Data {
		event := map[string]any{"type": kind + ".completed", "b64_json": image.B64JSON}
		if r.Created != 0 {
			event["created_at"] = r.Created
		}
		for name, value := range map[string]string{"background": r.Background, "output_format": r.OutputFormat,
			"quality": r.Quality, "size": r.Size} {
			if value != "" {
				event[name] = value
			}
		}
		if len(r.Usage) > 0 {
			event["usage"] = r.Usage
		}
		data, err := json.Marshal(event)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", kind+".completed", data)
	}
	return b.String(), nil
}

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
