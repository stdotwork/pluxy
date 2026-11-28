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
	"os"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/xai"
	version = "0.1.0"
)

const defaultBaseURL = "https://api.x.ai/v1"

const freeUsageCooldownMS = 24 * 60 * 60 * 1000

const cliChatBaseURL = "https://cli-chat-proxy.grok.com/v1"

const cliVersion = "1.0.46"

func main() {
	err := sdk.Serve(sdk.Extension{ID: id, Version: version, Generate: generate, Endpoint: endpoint,
		LoginMethods: loginMethods, Login: login, Refresh: refresh, Models: models, Check: check, Quota: quota})
	if err != nil {
		fmt.Fprintln(os.Stderr, "xai:", err)
		os.Exit(1)
	}
}

type settings struct {
	InjectXSearch bool   `json:"inject_x_search"`
	BaseURL       string `json:"base_url"`
	ClientVersion string `json:"client_version"`
	AuthIssuer    string `json:"auth_issuer"`
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
	if s.ClientVersion == "" {
		s.ClientVersion = cliVersion
	}
	if s.AuthIssuer = strings.TrimRight(sdk.TestLoginHost(s.AuthIssuer), "/"); s.AuthIssuer == "" {
		s.AuthIssuer = defaultIssuer
	}
	return s, nil
}

var client = &http.Client{
	Transport: sdk.Upstream(&http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	},
}

type target struct {
	base, token string
	cli         bool
}

func accountTarget(s settings, c *protocol.Credential) (target, error) {
	switch {
	case c != nil && c.Kind == protocol.CredentialAPIKey && c.APIKey != "":
		return target{base: s.BaseURL, token: c.APIKey}, nil
	case c != nil && c.Kind == protocol.CredentialOAuth && c.AccessToken != "":
		base := s.BaseURL
		if base == defaultBaseURL {
			base = cliChatBaseURL
		}
		return target{base: base, token: c.AccessToken, cli: true}, nil
	}
	return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the account has no xAI API key or Grok login", Scope: llm.ScopeCredential}
}

func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	cred := call.Account.Credential
	if cred != nil && cred.Kind != protocol.CredentialAPIKey {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only an API key can be checked without a generation; a login is not checked", Scope: llm.ScopeCredential}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	tg, err := accountTarget(s, cred)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tg.base+"/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+tg.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pluxy-xai/"+version)
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
		Data []json.RawMessage `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	}
	return &protocol.CheckResult{Detail: fmt.Sprintf("the upstream lists %d models", len(list.Data))}, nil
}

func setCLIIdentity(req *http.Request, s settings) {
	req.Header.Set("User-Agent", "xai-grok-workspace/"+s.ClientVersion)
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-version", s.ClientVersion)
	req.Header.Set("x-grok-client-identifier", "grok-shell")
	req.Header.Set("x-authenticateresponse", "authenticate-response")
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	tg, err := accountTarget(s, call.Account.Credential)
	if err != nil {
		return err
	}

	body, tr, bridge, err := buildRequest(call.Request, s.InjectXSearch)
	if err != nil {
		return err
	}
	rep := &reporting{Stream: out, tr: tr}
	var send eventSender = rep
	if bridge != nil {
		send = bridgeSender{b: bridge, next: rep}
	}
	url := tg.base + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tg.token)
	if tg.cli {
		setCLIIdentity(req, s)
	} else {
		req.Header.Set("User-Agent", "pluxy-xai/"+version)
	}
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if conv := promptCacheKey(body); conv != "" {
		req.Header.Set("x-grok-conv-id", conv)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return upstreamError(resp, call.Request.Model)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, &searchDecoder{streamDecoder: openairesponses.NewStreamDecoder(), filter: newSearchFilter(body)})
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	raw = newSearchFilter(body).response(raw)
	events, err := openairesponses.DecodeResponse(raw)
	if err != nil {
		return err
	}
	if bridge != nil {
		if events, err = convert(bridge, events); err != nil {
			return err
		}
		send = rep
	}
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

type eventSender interface{ Send(llm.Event) error }

func convert(b transformer, events []llm.Event) ([]llm.Event, error) {
	var out []llm.Event
	for _, ev := range events {
		more, err := b.Transform(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

func promptCacheKey(body []byte) string {
	var m struct {
		Key string `json:"prompt_cache_key"`
	}
	_ = json.Unmarshal(body, &m)
	return strings.TrimSpace(m.Key)
}

type reporting struct {
	*sdk.Stream
	tr *llm.Translation
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		ev.Response = &info
	}
	return r.Stream.Send(ev)
}

type streamDecoder interface {
	Decode(data []byte) ([]llm.Event, error)
	Close() ([]llm.Event, error)
	Usage() *llm.Usage
}

func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }, dec streamDecoder) error {
	r := sse.NewReader(body)
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
				return err
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

func upstreamError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Code  any             `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	var message, errType string
	var nested struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if json.Unmarshal(parsed.Error, &nested) == nil {
		message, errType = nested.Message, nested.Type
		if parsed.Code == nil {
			parsed.Code = nested.Code
		}
	} else {
		_ = json.Unmarshal(parsed.Error, &message)
	}
	e := &llm.Error{Status: resp.StatusCode, Message: message, ProviderCode: errType}
	if e.ProviderCode == "" && parsed.Code != nil {
		e.ProviderCode = fmt.Sprint(parsed.Code)
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("upstream returned %s", resp.Status)
	}
	if json.Valid(raw) {
		e.Raw = raw
	}
	lower := strings.ToLower(string(raw))
	switch s := resp.StatusCode; {
	case s >= 300 && s < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("upstream redirected (%s) to another host; not followed", resp.Status)
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden && badCredentials(lower):
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			e.Scope = llm.ScopeCredential
		}
		if freeUsageUsedUp(lower) {
			e.Code, e.RetryAfterMS = llm.CodeQuota, freeUsageCooldownMS
			break
		}
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s == http.StatusServiceUnavailable || s == 529:
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

func badCredentials(lower string) bool {
	return strings.Contains(lower, "bad-credentials") || strings.Contains(lower, "access token could not be validated")
}

func freeUsageUsedUp(lower string) bool {
	return strings.Contains(lower, "free-usage-exhausted") || strings.Contains(lower, "included free usage")
}
