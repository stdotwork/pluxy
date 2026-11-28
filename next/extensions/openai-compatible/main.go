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
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/openai-compatible"
	version = "0.1.0"
)

func main() {
	if err := sdk.Serve(sdk.Extension{ID: id, Version: version, Login: login, LoginMethods: loginMethods, Generate: maskGoogle(generate), CountTokens: maskGoogle(countTokens), Models: listModels, Check: check, Quota: quota, Endpoint: maskGoogle(endpointCall)}); err != nil {
		fmt.Fprintln(os.Stderr, "openai-compatible:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL           string   `json:"base_url"`
	WireAPI           string   `json:"wire_api"`
	ReasoningLevels   []string `json:"reasoning_levels"`
	DeveloperRole     string   `json:"developer_role"`
	StringContent     *bool    `json:"string_content"`
	ReasoningPassback *bool    `json:"reasoning_passback"`
	Quota             string   `json:"quota"`
}

const (
	wireChat      = "chat"
	wireResponses = "responses"
	wireMessages  = "messages"
	wireGemini    = "gemini"
)

const anthropicVersion = "2023-06-01"

var wires = map[string]wireInfo{
	wireChat:      {llm.FormatOpenAIChat, "/chat/completions"},
	wireResponses: {llm.FormatOpenAIResponses, "/responses"},
	wireMessages:  {llm.FormatAnthropicMessages, "/messages"},
	wireGemini:    {llm.FormatGeminiGenerate, "/models/"},
}

func endpoint(wire string, req llm.Request) string {
	e := wires[wire].endpoint
	if wire != wireGemini {
		return e
	}
	if req.Stream {
		return e + url.PathEscape(req.Model) + ":streamGenerateContent?alt=sse"
	}
	return e + url.PathEscape(req.Model) + ":generateContent"
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host: Go drops Authorization on a redirect to another host but not x-api-key or
// x-goog-api-key, so following one would send the account's key elsewhere.
var client = &http.Client{
	Transport: sdk.Upstream(&http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}),
	CheckRedirect: sameOrigin,
}

func sameOrigin(req *http.Request, via []*http.Request) error {
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

type wireInfo struct{ format, endpoint string }

func account(call sdk.Call) (settings, wireInfo, string, error) {
	var s settings
	if len(call.Account.Settings) > 0 {
		if err := json.Unmarshal(call.Account.Settings, &s); err != nil {
			return s, wireInfo{}, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	if s.BaseURL == "" {
		return s, wireInfo{}, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings need base_url", Scope: llm.ScopeCredential}
	}
	if s.WireAPI == "" {
		s.WireAPI = wireChat
	}
	if reason := checkShaping(s); reason != "" {
		return s, wireInfo{}, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: reason, Scope: llm.ScopeCredential}
	}
	wire, ok := wires[s.WireAPI]
	if !ok {
		return s, wireInfo{}, "", &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("account setting wire_api must be %q, %q, %q or %q", wireChat, wireResponses, wireMessages, wireGemini), Scope: llm.ScopeCredential}
	}
	if _, err := quotaVendor(s); err != nil {
		return s, wireInfo{}, "", err
	}
	cred := call.Account.Credential
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return s, wireInfo{}, "", &llm.Error{Code: llm.CodeAuth, Message: "account has no API key", Scope: llm.ScopeCredential}
	}
	return s, wire, cred.APIKey, nil
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	body, tr, err := upstreamBody(call.Request, wire.format, s)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr, names: replyNames(call.Request, tr), calls: openairesponses.NewCustomCallCheck(call.Request, tr)}
	resp, err := postBody(ctx, call, s, wire, key, endpoint(s.WireAPI, call.Request), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	send.headers = llm.ExtractRateLimitHeaders(wire.format, resp.Header)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, wire.format, call.Request.BodyReachesClient(wire.format, true))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, losses, err := decodeResponse(wire.format, raw)
	if err != nil {
		return llm.MarkContextOverflow(err)
	}
	for _, l := range llm.ReplyLosses(losses, call.Request.BodyReachesClient(wire.format, false)) {
		logReplyLoss(l)
	}
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func upstreamBody(req llm.Request, format string, s settings) ([]byte, *llm.Translation, error) {
	body, tr, err := buildBody(req, format, s)
	if err != nil {
		return nil, nil, err
	}
	sent := body
	var losses []llm.Loss
	if body, losses, err = applyReasoning(body, req, format, s.ReasoningLevels); err != nil {
		return nil, nil, err
	}
	for _, l := range losses {
		tr.Losses = llm.AddLoss(tr.Losses, l)
	}
	if format == llm.FormatOpenAIChat {
		if body, losses, err = shapeChat(body, s); err != nil {
			return nil, nil, err
		}
		for _, l := range losses {
			tr.Losses = llm.AddLoss(tr.Losses, l)
		}
	}
	if tr.Passthrough && (len(tr.Losses) > 0 || !bytes.Equal(sent, body)) {
		tr.Passthrough = false
	}
	return body, tr, nil
}

func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, wire, key, err := account(sdk.Call{ID: call.ID, Account: call.Account})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setKey(req, wire, key, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, upstreamError(resp, "")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream model list: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Created     int64  `json:"created"`
			OwnedBy     string `json:"owned_by"`
		} `json:"data"`
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			Description                string   `json:"description"`
			Version                    string   `json:"version"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || (payload.Data == nil && payload.Models == nil) {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream model list is not an OpenAI or Gemini list", Scope: llm.ScopeEndpoint}
	}
	models := make([]protocol.Model, 0, len(payload.Data)+len(payload.Models))
	seen := map[string]bool{}
	add := func(m protocol.Model) {
		if m.ID == "" || seen[m.ID] {
			return
		}
		seen[m.ID] = true
		models = append(models, m)
	}
	for _, item := range payload.Data {
		add(protocol.Model{
			ID:          strings.TrimSpace(item.ID),
			DisplayName: strings.TrimSpace(item.DisplayName),
			Created:     item.Created,
			OwnedBy:     strings.TrimSpace(item.OwnedBy),
		})
	}
	for _, item := range payload.Models {
		if len(item.SupportedGenerationMethods) > 0 && !slices.ContainsFunc(item.SupportedGenerationMethods, generationMethod) {
			continue
		}
		name := strings.TrimSpace(item.Name)
		id := strings.TrimPrefix(name, "models/")
		m := protocol.Model{
			ID:                id,
			DisplayName:       strings.TrimSpace(item.DisplayName),
			Description:       strings.TrimSpace(item.Description),
			Version:           strings.TrimSpace(item.Version),
			InputTokenLimit:   max(item.InputTokenLimit, 0),
			OutputTokenLimit:  max(item.OutputTokenLimit, 0),
			GenerationMethods: item.SupportedGenerationMethods,
		}
		if id != "" && name != id {
			m.Name = "models/" + id
		}
		add(m)
	}
	return &protocol.ModelList{Models: models}, nil
}

func generationMethod(method string) bool {
	return method == "generateContent" || method == "streamGenerateContent"
}

func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	s, wire, key, err := account(sdk.Call{ID: call.ID, Account: call.Account})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setKey(req, wire, key, nil)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "the upstream has no model list to check the key with", Scope: llm.ScopeCredential}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, upstreamError(resp, "")
	}
	var list struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if json.Unmarshal(raw, &list) != nil || (list.Data == nil && list.Models == nil) {
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	}
	return &protocol.CheckResult{Detail: fmt.Sprintf("the upstream lists %d models", len(list.Data)+len(list.Models))}, nil
}

func setKey(req *http.Request, wire wireInfo, key string, native *llm.Native) {
	switch wire.format {
	case llm.FormatGeminiGenerate:
		req.Header.Set("X-Goog-Api-Key", key)
	case llm.FormatAnthropicMessages:
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Anthropic-Version", anthropicVersion)
		if native != nil && native.Format == wire.format {
			for _, h := range anthropicmessages.Headers {
				if v := native.Headers[h]; v != "" {
					req.Header.Set(h, v)
				}
			}
		}
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

func postBody(ctx context.Context, call sdk.Call, s settings, wire wireInfo, key, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	setKey(req, wire, key, call.Request.Native)
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, requestFailed(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		e := upstreamError(resp, call.Request.Model)
		e.Headers = llm.ExtractRateLimitHeaders(wire.format, resp.Header)
		return nil, e
	}
	return resp, nil
}

func requestFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
}

func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, wire, key, err := account(call)
	if err != nil {
		return err
	}
	req := call.Request
	req.Stream = false
	call.Request = req
	var path string
	switch wire.format {
	case llm.FormatAnthropicMessages:
		path = "/messages/count_tokens"
	case llm.FormatGeminiGenerate:
		path = "/models/" + url.PathEscape(req.Model) + ":countTokens"
	default:
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's upstream speaks " + formatNames[wire.format] + ", which has no token count endpoint"}
	}
	body, tr, err := buildBody(req, wire.format, s)
	if err != nil {
		return err
	}
	count := anthropicmessages.CountRequest
	switch {
	case tr.Passthrough && wire.format == llm.FormatAnthropicMessages:
		count = anthropicmessages.CountBody
	case tr.Passthrough:
		count = nil
	}
	if count != nil {
		if body, err = count(body); err != nil {
			return err
		}
	}
	resp, err := postBody(ctx, call, s, wire, key, path, body)
	if err != nil {
		return noCountEndpoint(err, "token count", path)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	var n int64
	if wire.format == llm.FormatAnthropicMessages {
		if n, err = anthropicmessages.DecodeCount(raw); err != nil {
			return err
		}
	} else {
		var r struct {
			TotalTokens *int64 `json:"totalTokens"`
		}
		if json.Unmarshal(raw, &r) != nil || r.TotalTokens == nil {
			return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "the upstream's countTokens reply has no totalTokens", Raw: llm.RawJSON(raw)}
		}
		n = *r.TotalTokens
	}
	if err := out.Start(llm.ResponseInfo{
		Model:   req.Model,
		Format:  wire.format,
		Headers: llm.ExtractRateLimitHeaders(wire.format, resp.Header),
	}); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: &n, Raw: llm.RawJSON(raw)})
}

var formatNames = map[string]string{
	llm.FormatOpenAIChat:        "Chat Completions",
	llm.FormatOpenAIResponses:   "OpenAI Responses",
	llm.FormatAnthropicMessages: "Anthropic Messages",
	llm.FormatGeminiGenerate:    "Gemini GenerateContent",
}

func buildBody(req llm.Request, target string, s settings) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: target}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == target {
			tr.Passthrough = true
			return n.Body, tr, nil
		}
	}
	f := tierFieldsOf(s, target)
	var encode func(llm.Request) ([]byte, []llm.Loss, error)
	switch target {
	case llm.FormatOpenAIChat:
		encode = func(r llm.Request) ([]byte, []llm.Loss, error) {
			return openaichat.EncodeRequestWith(r, openaichat.EncodeOptions{ServiceTier: f.serviceTier, Verbosity: f.verbosity, WebSearch: f.webSearch, CandidateCount: f.candidateCount, AssistantAnnotations: f.assistantAnnotations})
		}
	case llm.FormatOpenAIResponses:
		encode = func(r llm.Request) ([]byte, []llm.Loss, error) {
			return openairesponses.EncodeRequestWith(r, openairesponses.EncodeOptions{ServiceTier: f.serviceTier, Verbosity: f.verbosity, WebSearch: f.webSearch, CodeExecution: f.codeExecution})
		}
	case llm.FormatAnthropicMessages:
		encode = func(r llm.Request) ([]byte, []llm.Loss, error) {
			return anthropicmessages.EncodeRequestWith(r, anthropicmessages.EncodeOptions{ServiceTier: f.serviceTier, WebSearch: f.webSearch, CodeExecution: f.codeExecution, StructuredOutputRules: f.structuredOutput})
		}
	case llm.FormatGeminiGenerate:
		encode = func(r llm.Request) ([]byte, []llm.Loss, error) {
			return geminigenerate.EncodeRequestWith(r, geminigenerate.EncodeOptions{ServiceTier: f.serviceTier, WebSearch: f.webSearch, CodeExecution: f.codeExecution, CandidateCount: f.candidateCount})
		}
	default:
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "the upstream speaks an unknown format " + strconv.Quote(target)}
	}
	body, losses, err := encode(req)
	if err != nil {
		return nil, nil, err
	}
	tr.Losses = losses
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		paths := tr.DroppedPaths()
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
			Message: formatNames[target] + " cannot carry parts of this request: " + strings.Join(paths, ", ")}
	}
	return body, tr, nil
}

type reporting struct {
	*sdk.Stream
	tr      *llm.Translation
	headers map[string]string
	names   map[string]string
	calls   *openairesponses.CustomCallCheck
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		info.Headers = r.headers
		ev.Response = &info
	}
	r.restore(&ev)
	if err := r.calls.Check(ev); err != nil {
		return err
	}
	return r.Stream.Send(ev)
}

func (r *reporting) restore(ev *llm.Event) {
	if ev.Type == llm.EventCandidate && ev.Candidate != nil && ev.Candidate.Kind == llm.CandidateToolCallStart {
		if name, ok := r.names[ev.Candidate.Name]; ok {
			c := *ev.Candidate
			c.Name = name
			ev.Candidate = &c
		}
		return
	}
	if ev.Type != llm.EventItemStart || ev.Item == nil || ev.Item.Kind != llm.ItemToolCall {
		return
	}
	if name, ok := r.names[ev.Item.Name]; ok {
		item := *ev.Item
		item.Name = name
		ev.Item = &item
	}
}

func replyNames(req llm.Request, tr *llm.Translation) map[string]string {
	if tr.Passthrough {
		return nil
	}
	var changed map[string]string
	switch tr.To {
	case llm.FormatOpenAIChat:
		changed = openaichat.ToolNames(req)
	case llm.FormatGeminiGenerate:
		changed = geminigenerate.ToolNames(req)
	case llm.FormatAnthropicMessages:
		changed = anthropicmessages.ToolNames(req)
	default:
		return nil
	}
	back := map[string]string{}
	for from, to := range changed {
		back[to] = from
	}
	return back
}

type eventSender interface{ Send(llm.Event) error }

type streamDecoder interface {
	Decode(data []byte) ([]llm.Event, error)
	Close() ([]llm.Event, error)
	Usage() *llm.Usage
}

var decoders = map[string]struct {
	stream   func() streamDecoder
	response func([]byte) ([]llm.Event, error)
}{
	llm.FormatOpenAIChat:        {func() streamDecoder { return openaichat.NewStreamDecoder() }, openaichat.DecodeResponse},
	llm.FormatOpenAIResponses:   {func() streamDecoder { return openairesponses.NewStreamDecoder() }, openairesponses.DecodeResponse},
	llm.FormatAnthropicMessages: {func() streamDecoder { return anthropicmessages.NewStreamDecoder() }, anthropicmessages.DecodeResponse},
	llm.FormatGeminiGenerate:    {func() streamDecoder { return geminigenerate.NewStreamDecoder() }, geminigenerate.DecodeResponse},
}

func relayStream(ctx context.Context, body io.Reader, out eventSender, upstreamFormat string, bodyReaches bool) error {
	r := sse.NewReader(body)
	dec := decoders[upstreamFormat].stream()
	defer reportLosses(dec, bodyReaches)
	for {
		e, err := r.Next()
		eof := err == io.EOF
		var events []llm.Event
		switch {
		case eof:
			events, err = dec.Close()
			if err != nil {
				return err
			}
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream broke: " + err.Error(), Scope: llm.ScopeEndpoint, Usage: dec.Usage()}
		default:
			events, err = dec.Decode([]byte(e.Data))
			if err != nil {
				return llm.MarkContextOverflow(err)
			}
		}
		for _, ev := range events {
			if err := out.Send(ev); err != nil {
				return err
			}
			if ev.Terminal() {
				return nil
			}
		}
		if eof {
			return nil
		}
	}
}

func reportLosses(dec streamDecoder, bodyReaches bool) {
	lr, ok := dec.(interface{ Losses() []llm.Loss })
	if !ok {
		return
	}
	for _, l := range llm.ReplyLosses(lr.Losses(), bodyReaches) {
		logReplyLoss(l)
	}
}

func logReplyLoss(l llm.Loss) {
	fmt.Fprintf(os.Stderr, "reply loss: %s %s (%s)\n", l.Kind, l.Path, l.Detail)
}

func decodeResponse(format string, raw []byte) ([]llm.Event, []llm.Loss, error) {
	switch format {
	case llm.FormatOpenAIChat:
		return openaichat.DecodeResponseLosses(raw)
	case llm.FormatGeminiGenerate:
		return geminigenerate.DecodeResponseLosses(raw)
	case llm.FormatOpenAIResponses:
		return openairesponses.DecodeResponseLosses(raw)
	case llm.FormatAnthropicMessages:
		return anthropicmessages.DecodeResponseLosses(raw)
	}
	events, err := decoders[format].response(raw)
	return events, nil, err
}

func upstreamError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message}
	if c := parsed.Error.Code; parsed.Error.Status != "" {
		e.ProviderCode = parsed.Error.Status
	} else if c != nil {
		e.ProviderCode = fmt.Sprint(c)
	} else {
		e.ProviderCode = parsed.Error.Type
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("upstream returned %s", resp.Status)
	}
	if json.Valid(raw) {
		e.Raw = raw
	}
	switch s := resp.StatusCode; {
	case s >= 300 && s < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("upstream redirected (%s) to another host; not followed", resp.Status)
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		if modelRefused(e, model) {
			e.Retryable = true
		}
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			e.Scope = llm.ScopeCredential
		}
		if e.ProviderCode == "insufficient_quota" {
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s == http.StatusServiceUnavailable:
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
		if s == 529 {
			e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
		_ = llm.MarkContextOverflow(e)
	}
	return e
}

func modelRefused(e *llm.Error, model string) bool {
	return e.ProviderCode == "model_not_found" || (model != "" && strings.Contains(e.Message, model))
}
