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
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/claude"
	version = "0.1.0"
)

const defaultBaseURL = "https://api.anthropic.com"

const anthropicVersion = "2023-06-01"

// oauthBeta is the beta Anthropic requires with an OAuth access token.
const oauthBeta = "oauth-2025-04-20"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		CountTokens:  countTokens,
		Models:       listModels,
		Check:        check,
		Quota:        quota,
		Endpoint:     endpoint,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL                 string `json:"base_url"`
	AuthorizeURL            string `json:"authorize_url"`
	TokenURL                string `json:"token_url"`
	ClientID                string `json:"client_id"`
	RebuildMidSystemMessage bool   `json:"rebuild_mid_system_message"`
	ModelLevelCooling       bool   `json:"model_level_cooling"`
	CacheTTL                string `json:"cache_ttl"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	for _, suffix := range []string{"/v1/messages", "/v1"} {
		if base, ok := strings.CutSuffix(s.BaseURL, suffix); ok {
			s.BaseURL = base
			break
		}
	}
	if s.AuthorizeURL = sdk.TestLoginHost(s.AuthorizeURL); s.AuthorizeURL == "" {
		s.AuthorizeURL = defaultAuthorizeURL
	}
	if s.TokenURL = sdk.TestLoginHost(s.TokenURL); s.TokenURL == "" {
		s.TokenURL = defaultTokenURL
	}
	if s.ClientID == "" {
		s.ClientID = defaultClientID
	}
	s.CacheTTL = strings.ToLower(strings.TrimSpace(s.CacheTTL))
	switch s.CacheTTL {
	case "", cacheTTL5m:
		s.CacheTTL = cacheTTL5m
	case cacheTTL1h:
	default:
		return s, &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeCredential,
			Message: fmt.Sprintf("cache_ttl must be %q or %q, not %q", cacheTTL5m, cacheTTL1h, s.CacheTTL)}
	}
	return s, nil
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, since Go keeps x-api-key on a redirect to another host.
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

func authorize(h http.Header, cred *protocol.Credential) error {
	switch {
	case cred == nil:
	case cred.Kind == protocol.CredentialAPIKey && isOAuthToken(cred.APIKey):
		bearer(h, cred.APIKey)
		return nil
	case cred.Kind == protocol.CredentialAPIKey && cred.APIKey != "":
		h.Set("X-Api-Key", cred.APIKey)
		return nil
	case cred.Kind == protocol.CredentialOAuth && cred.AccessToken != "":
		bearer(h, cred.AccessToken)
		return nil
	}
	return &llm.Error{Code: llm.CodeAuth, Message: "the account has no usable credential; add an API key or log in again", Scope: llm.ScopeCredential}
}

func bearer(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
	h.Set("Anthropic-Beta", withBeta(h.Get("Anthropic-Beta"), oauthBeta))
}

func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func withBeta(list, beta string) string {
	var out []string
	for _, b := range strings.Split(list, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	if !slices.Contains(out, beta) {
		out = append(out, beta)
	}
	return strings.Join(out, ",")
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBodyFor(call.Request, s, serviceOptions(anthropicmessages.EncodeOptions{
		SearchResultOwned: func(enc string) bool { return searchOwners.mintedBy(enc, call.Account.ID) },
	}, call.Request, s.BaseURL))
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr, names: replyNames(call.Request, tr), account: call.Account.ID,
		calls: openairesponses.NewCustomCallCheck(call.Request, tr)}
	resp, err := post(ctx, call, s, "/v1/messages", body, tr.Passthrough, call.Request.Stream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	send.headers = llm.ExtractRateLimitHeaders(llm.FormatAnthropicMessages, resp.Header)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, call.Request.BodyReachesClient(llm.FormatAnthropicMessages, true))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, losses, err := anthropicmessages.DecodeResponseLosses(raw)
	for _, l := range llm.ReplyLosses(losses, call.Request.BodyReachesClient(llm.FormatAnthropicMessages, false)) {
		fmt.Fprintf(os.Stderr, "reply loss: %s %s (%s)\n", l.Kind, l.Path, l.Detail)
	}
	if err != nil {
		return llm.MarkContextOverflow(err)
	}
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func post(ctx context.Context, call sdk.Call, s settings, path string, body []byte, passthrough, stream bool) (*http.Response, error) {
	body, betas := bodyBetas(body, path == "/v1/messages/count_tokens")
	// ?beta=true is how Anthropic's SDKs, Claude Code among them, call the API with betas.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+path+"?beta=true", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Accept-Encoding", "identity")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if passthrough {
		names := slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders)
		if anthropicmessages.IsClaudeCode(call.Request.Native.Headers) {
			names = append(names, anthropicmessages.ClaudeCodeHeaders...)
		}
		for _, h := range names {
			if v := call.Request.Native.Headers[h]; v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	fast := isFast(body, req.Header.Get("Anthropic-Beta")) || slices.Contains(betas, fastBeta)
	if fast {
		req.Header.Set("Anthropic-Beta", withBeta(req.Header.Get("Anthropic-Beta"), fastBeta))
	}
	if err := authorize(req.Header, call.Account.Credential); err != nil {
		return nil, err
	}
	if list := addBetas(req.Header.Get("Anthropic-Beta"), betas); list != "" {
		req.Header.Set("Anthropic-Beta", list)
	}
	fail := func(e *llm.Error) *llm.Error {
		if fast && isAnthropicBase(s.BaseURL) {
			return fastFailure(e)
		}
		return e
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail(&llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)})
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, fail(upstreamError(resp, call.Request.Model, s.ModelLevelCooling))
	}
	return resp, nil
}

func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	cred := call.Account.Credential
	if cred != nil && cred.Kind != protocol.CredentialAPIKey {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only an API key can be checked without a generation; a login is not checked", Scope: llm.ScopeCredential}
	}
	if cred != nil && isOAuthToken(cred.APIKey) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "a setup token cannot be checked without a generation; it is not checked", Scope: llm.ScopeCredential}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if err := authorize(req.Header, cred); err != nil {
		return nil, err
	}
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
		return nil, upstreamError(resp, "", s.ModelLevelCooling)
	}
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	}
	return &protocol.CheckResult{Detail: fmt.Sprintf("the upstream lists %d models", len(list.Data))}, nil
}

func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	if !isAnthropicBase(s.BaseURL) {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's base_url is not Anthropic's own (" + defaultBaseURL + "), which is the only one this extension sends a token count to"}
	}
	body, tr, err := buildBodyFor(call.Request, s, serviceOptions(anthropicmessages.EncodeOptions{
		SearchResultOwned: func(enc string) bool { return searchOwners.mintedBy(enc, call.Account.ID) },
	}, call.Request, s.BaseURL))
	if err != nil {
		return err
	}
	count := anthropicmessages.CountRequest
	if tr.Passthrough {
		count = anthropicmessages.CountBody
	}
	if body, err = count(body); err != nil {
		return err
	}
	resp, err := post(ctx, call, s, "/v1/messages/count_tokens", body, tr.Passthrough, false)
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
	n, err := anthropicmessages.DecodeCount(raw)
	if err != nil {
		return err
	}
	if err := out.Start(llm.ResponseInfo{
		Model:   call.Request.Model,
		Format:  llm.FormatAnthropicMessages,
		Headers: llm.ExtractRateLimitHeaders(llm.FormatAnthropicMessages, resp.Header),
	}); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: &n, Raw: llm.RawJSON(raw)})
}

func isAnthropicBase(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.anthropic.com") {
		return false
	}
	port := u.Port()
	return port == "" || port == "443"
}

func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	return buildBodyWithTTL(req, cacheTTL5m)
}

func buildBodyWithTTL(req llm.Request, cacheTTL string) ([]byte, *llm.Translation, error) {
	return buildBodyWith(req, cacheTTL, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
}

// Only Anthropic's own API is known to take a service tier: a third-party host may refuse a
// field it does not list.
func serviceOptions(opts anthropicmessages.EncodeOptions, req llm.Request, baseURL string) anthropicmessages.EncodeOptions {
	anthropic := isAnthropicBase(baseURL)
	opts.ServiceTier = anthropic
	opts.FastMode = anthropic || req.SourceFormat() == llm.FormatOpenAIResponses
	opts.WebSearch = anthropic
	opts.CodeExecution = anthropic
	opts.StructuredOutputRules = anthropic
	return opts
}

func buildBodyWith(req llm.Request, cacheTTL string, opts anthropicmessages.EncodeOptions) ([]byte, *llm.Translation, error) {
	return buildBodyFor(req, settings{CacheTTL: cacheTTL}, opts)
}

func buildBodyFor(req llm.Request, s settings, opts anthropicmessages.EncodeOptions) ([]byte, *llm.Translation, error) {
	cacheTTL := s.CacheTTL
	tr := &llm.Translation{To: llm.FormatAnthropicMessages}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == llm.FormatAnthropicMessages {
			tr.Passthrough = true
			body := n.Body
			var losses []llm.Loss
			if suf := req.Options.ThinkingSuffix; suf != "" {
				var err error
				if body, losses, _, err = anthropicmessages.ApplyEffortReport(body, req.Model, suf); err != nil {
					return nil, nil, err
				}
			}
			body, err := fixBody(body)
			if err != nil {
				return nil, nil, err
			}
			if s.RebuildMidSystemMessage {
				if body, err = moveSystemMessages(body); err != nil {
					return nil, nil, err
				}
			}
			var repaired, fitted []llm.Loss
			if body, repaired, err = anthropicmessages.RepairTurns(body, req.Model); err != nil {
				return nil, nil, err
			}
			if body, fitted, err = anthropicmessages.FitThinking(body, req.Model); err != nil {
				return nil, nil, err
			}
			for _, l := range append(append(losses, repaired...), fitted...) {
				tr.Losses = llm.AddLoss(tr.Losses, l)
			}
			if err := refuseDrops(req, tr); err != nil {
				return nil, nil, err
			}
			return body, tr, nil
		}
	}
	body, losses, err := anthropicmessages.EncodeRequestWith(req, opts)
	if err != nil {
		return nil, nil, err
	}
	if n, ok := maxOutputTokens(req.Model); ok && req.Options.MaxOutputTokens == nil {
		if body, err = setField(body, "max_tokens", n); err != nil {
			return nil, nil, err
		}
	}
	if body, losses, cacheTTL, err = applyClientCache(body, req, losses, cacheTTL); err != nil {
		return nil, nil, err
	}
	if body, err = addCacheBreakpointsTTL(body, cacheTTL); err != nil {
		return nil, nil, err
	}
	if body, err = fixBody(body); err != nil {
		return nil, nil, err
	}
	effort := req.Options.ReasoningEffort
	if s := req.Options.ThinkingSuffix; s != "" {
		effort = s
	}
	var more []llm.Loss
	if effort != "" {
		var applied bool
		body, more, applied, err = anthropicmessages.ApplyEffortReport(body, req.Model, effort)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			losses = slices.DeleteFunc(losses, func(l llm.Loss) bool { return l.Path == "reasoning_effort" })
		}
	}
	var summary, fitted, dropped []llm.Loss
	var carried bool
	if body, summary, carried, err = anthropicmessages.ApplySummary(body, req.Model, req); err != nil {
		return nil, nil, err
	}
	if carried {
		losses = slices.DeleteFunc(losses, func(l llm.Loss) bool { return l.Path == "reasoning" })
	}
	if body, fitted, err = anthropicmessages.FitThinking(body, req.Model); err != nil {
		return nil, nil, err
	}
	if body, dropped, err = anthropicmessages.DropRefusedSampling(body, req.Model); err != nil {
		return nil, nil, err
	}
	for _, l := range slices.Concat(more, summary, fitted, dropped) {
		losses = llm.AddLoss(losses, l)
	}
	tr.Losses = losses
	if err := refuseDrops(req, tr); err != nil {
		return nil, nil, err
	}
	if s.RebuildMidSystemMessage {
		if body, err = moveSystemMessages(body); err != nil {
			return nil, nil, err
		}
	}
	return body, tr, nil
}

func refuseDrops(req llm.Request, tr *llm.Translation) error {
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		paths := tr.DroppedPaths()
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
			Message: "Anthropic Messages cannot carry parts of this request: " + strings.Join(paths, ", ")}
	}
	return nil
}

func setField(body []byte, key string, v any) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	m[key] = raw
	return json.Marshal(m)
}

type reporting struct {
	*sdk.Stream
	tr      *llm.Translation
	headers map[string]string
	names   map[string]string
	account string
	calls   *openairesponses.CustomCallCheck
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		info.Headers = r.headers
		ev.Response = &info
	}
	if ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
		if name, ok := r.names[ev.Item.Name]; ok {
			item := *ev.Item
			item.Name = name
			ev.Item = &item
		}
	}
	if err := r.calls.Check(ev); err != nil {
		return err
	}
	rememberSearchResults(ev.Raw, r.account)
	return r.Stream.Send(ev)
}

func replyNames(req llm.Request, tr *llm.Translation) map[string]string {
	if tr.Passthrough {
		return nil
	}
	back := map[string]string{}
	for from, to := range anthropicmessages.ToolNames(req) {
		back[to] = from
	}
	return back
}

func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }, bodyReaches bool) error {
	r := sse.NewReader(body)
	dec := anthropicmessages.NewStreamDecoder()
	defer func() {
		for _, l := range llm.ReplyLosses(dec.Losses(), bodyReaches) {
			fmt.Fprintf(os.Stderr, "reply loss: %s %s (%s)\n", l.Kind, l.Path, l.Detail)
		}
	}()
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

func upstreamError(resp *http.Response, model string, modelLevelCooling bool) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{
		Status:       resp.StatusCode,
		Message:      parsed.Error.Message,
		ProviderCode: parsed.Error.Type,
		Headers:      llm.ExtractRateLimitHeaders(llm.FormatAnthropicMessages, resp.Header),
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
	case isAccountClosed(s, parsed.Error.Type, e.Message):
		e.Code, e.Scope, e.Retryable = llm.CodeAccountClosed, llm.ScopeCredential, true
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		// Anthropic names the model when it does not have it: then it did not run the request,
		// and another account may serve the model.
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		var wait time.Duration
		e.Code, e.Scope, wait, e.QuotaWindowMinutes = rateLimit(resp.Header, raw, modelLevelCooling, time.Now())
		e.Retryable = true
		if model == "" && e.Scope == llm.ScopeModel {
			e.Scope = llm.ScopeCredential
		}
		if !(e.Code == llm.CodeQuota && e.Scope == llm.ScopeCredential) && fastCredits(raw) {
			e.Scope = llm.ScopeRequest
		}
		e.RetryAfterMS = int64((wait + time.Millisecond - 1) / time.Millisecond)
	case s == http.StatusServiceUnavailable || s == 529:
		// 529 is Anthropic's overloaded_error: the request was refused, not run.
		e.Code, e.Scope, e.Retryable = llm.CodeUnavailable, llm.ScopeEndpoint, true
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
		_ = llm.MarkContextOverflow(e)
	}
	return e
}

func isAccountClosed(status int, errType, msg string) bool {
	if strings.EqualFold(strings.TrimSpace(errType), "account_deactivated") {
		return true
	}
	if status != http.StatusBadRequest && status != http.StatusUnauthorized && status != http.StatusForbidden {
		return false
	}
	m := strings.ToLower(msg)
	for _, s := range []string{
		"organization has been disabled",
		"account has been disabled",
		"account has been deactivated",
		"this account is deactivated",
		"your account has been suspended",
		"credential has been disabled",
		"user has been disabled",
		"account_deactivated",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
