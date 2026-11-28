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
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/kimi"
	version = "0.1.0"
)

const anthropicVersion = "2023-06-01"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		Models:       models,
		Quota:        quota,
		CountTokens:  countTokens,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "kimi:", err)
		os.Exit(1)
	}
}

const (
	domainCom = "kimi.com"
	domainAI  = "kimi.ai"
)

type settings struct {
	Domain      string `json:"domain"`
	BaseURL     string `json:"base_url"`
	AuthBaseURL string `json:"auth_base_url"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	switch s.Domain {
	case "", domainCom:
		s.Domain = domainCom
	case domainAI:
	default:
		return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("account setting domain must be %q or %q", domainCom, domainAI), Scope: llm.ScopeCredential}
	}
	if s.BaseURL == "" {
		s.BaseURL = "https://api." + s.Domain + "/coding"
	}
	if s.AuthBaseURL = sdk.TestLoginHost(s.AuthBaseURL); s.AuthBaseURL == "" {
		s.AuthBaseURL = "https://auth." + s.Domain
	}
	s.BaseURL = strings.TrimSuffix(strings.TrimRight(s.BaseURL, "/"), "/v1")
	s.AuthBaseURL = strings.TrimRight(s.AuthBaseURL, "/")
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

func deviceHeaders(h http.Header, deviceID string) {
	h.Set("X-Msh-Platform", "pluxy")
	h.Set("X-Msh-Version", version)
	h.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
	if deviceID != "" {
		h.Set("X-Msh-Device-Id", deviceID)
	}
}

type credentialData struct {
	DeviceID string `json:"device_id"`
}

type upstream struct {
	format, path string
}

var (
	upChat      = upstream{llm.FormatOpenAIChat, "/v1/chat/completions"}
	upResponses = upstream{llm.FormatOpenAIResponses, "/v1/responses"}
	// ?beta=true is how Anthropic's SDKs call Messages with betas; Kimi takes it the same way.
	upMessages = upstream{llm.FormatAnthropicMessages, "/v1/messages?beta=true"}
)

func bearer(c *protocol.Credential) (string, credentialData, error) {
	var data credentialData
	switch {
	case c == nil:
	case c.Kind == protocol.CredentialOAuth && c.AccessToken != "":
		_ = json.Unmarshal(c.Data, &data)
		return c.AccessToken, data, nil
	case c.Kind == protocol.CredentialAPIKey && c.APIKey != "":
		return c.APIKey, data, nil
	}
	return "", data, &llm.Error{Code: llm.CodeAuth, Message: "the account has no Kimi login or API key", Scope: llm.ScopeCredential}
}

func upstreamFor(req llm.Request) upstream {
	if req.Native != nil {
		switch req.Native.Format {
		case llm.FormatOpenAIResponses:
			return upResponses
		case llm.FormatAnthropicMessages:
			return upMessages
		}
	}
	return upChat
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	token, data, err := bearer(call.Account.Credential)
	if err != nil {
		return err
	}

	up := upstreamFor(call.Request)
	var turn *replayTurn
	if up == upMessages {
		turn = newReplayTurn(replays, call.Request)
	}
	body, tr, err := buildBody(call.Request, up, turn)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr, names: replyNames(call.Request, tr), calls: openairesponses.NewCustomCallCheck(call.Request, tr)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+up.path, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
		if up == upMessages {
			req.Header.Set("Accept-Encoding", "identity")
		}
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if up == upMessages {
		req.Header.Set("Anthropic-Version", anthropicVersion)
		for _, h := range slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders) {
			if v := call.Request.Native.Headers[h]; v != "" {
				req.Header.Set(h, v)
			}
		}
	} else {
		req.Header.Set("User-Agent", "pluxy-kimi/"+version)
		deviceHeaders(req.Header, data.DeviceID)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		turn.rejected(resp.StatusCode)
		return upstreamError(resp, call.Request.Model)
	}
	dec := decoders[up.format]
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if turn == nil {
			return relayStream(ctx, resp.Body, send, dec.stream(), call.Request.BodyReachesClient(up.format, true))
		}
		capture := newReplayCapture()
		err := relayStream(ctx, resp.Body, send, replayDecoder{dec.stream(), capture, turn}, call.Request.BodyReachesClient(up.format, true))
		if capture.failed {
			turn.forget()
		}
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, losses, err := dec.response(raw)
	if err != nil {
		return err
	}
	for _, l := range llm.ReplyLosses(losses, call.Request.BodyReachesClient(up.format, false)) {
		logReplyLoss(l)
	}
	turn.keepResponse(raw)
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

type reporting struct {
	*sdk.Stream
	tr    *llm.Translation
	names map[string]string
	calls *openairesponses.CustomCallCheck
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		ev.Response = &info
	}
	r.restore(&ev)
	if err := r.calls.Check(ev); err != nil {
		return err
	}
	return r.Stream.Send(ev)
}

func (r *reporting) restore(ev *llm.Event) {
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
	back := map[string]string{}
	for from, to := range openaichat.ToolNames(req) {
		back[to] = from
	}
	return back
}

type streamDecoder interface {
	Decode(data []byte) ([]llm.Event, error)
	Close() ([]llm.Event, error)
	Usage() *llm.Usage
}

var decoders = map[string]struct {
	stream   func() streamDecoder
	response func([]byte) ([]llm.Event, []llm.Loss, error)
}{
	llm.FormatOpenAIChat:        {func() streamDecoder { return openaichat.NewStreamDecoder() }, openaichat.DecodeResponseLosses},
	llm.FormatOpenAIResponses:   {func() streamDecoder { return openairesponses.NewStreamDecoder() }, openairesponses.DecodeResponseLosses},
	llm.FormatAnthropicMessages: {func() streamDecoder { return anthropicmessages.NewStreamDecoder() }, anthropicmessages.DecodeResponseLosses},
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

func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }, dec streamDecoder, bodyReaches bool) error {
	r := sse.NewReader(body)
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
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message, ProviderCode: parsed.Error.Type}
	if e.ProviderCode == "" && parsed.Error.Code != nil {
		e.ProviderCode = fmt.Sprint(parsed.Error.Code)
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
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			e.Scope = llm.ScopeCredential
		}
		if strings.Contains(e.ProviderCode, "quota") || strings.Contains(strings.ToLower(e.Message), "quota") {
			e.Code = llm.CodeQuota
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
