// Command kimi is pluxy's provider extension for Kimi Code, Moonshot AI's coding subscription,
// signed in with Kimi's device-code login (oauth.go).
//
// Account settings (all optional):
//
//	{"domain": "kimi.com", "base_url": "https://api.kimi.com/coding"}
//
// domain is kimi.com (the default) or kimi.ai, Kimi's two sites, each with its own login and API.
// base_url is where the API lives; requests go to {base_url}/v1/chat/completions, /v1/responses or
// /v1/messages, whichever speaks the client's format (Gemini clients are translated to Chat
// Completions). auth_base_url changes the login host, for a test server.
//
// The account's access token, or a Kimi Code API key, is sent as a bearer token with Kimi's device
// headers (X-Msh-*), which name pluxy as the client and carry the device ID made at login. A request in the upstream's
// format passes through with the changes Kimi needs (body.go).
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
	"strconv"
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

// anthropicVersion is the Messages API version sent when the client named none.
const anthropicVersion = "2023-06-01"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "kimi:", err)
		os.Exit(1)
	}
}

// Kimi's sites.
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
	if s.AuthBaseURL == "" {
		s.AuthBaseURL = "https://auth." + s.Domain
	}
	// A base URL given with its /v1 is taken as the same place.
	s.BaseURL = strings.TrimSuffix(strings.TrimRight(s.BaseURL, "/"), "/v1")
	s.AuthBaseURL = strings.TrimRight(s.AuthBaseURL, "/")
	return s, nil
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host.
var client = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	},
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

// deviceHeaders sets the headers Kimi's API and login take from every client: the platform and
// version (pluxy's), the device model and the device ID made at login. The device's host name,
// which Kimi's own CLI also sends, is left out: it would name the user's machine to Kimi.
func deviceHeaders(h http.Header, deviceID string) {
	h.Set("X-Msh-Platform", "pluxy")
	h.Set("X-Msh-Version", version)
	h.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
	if deviceID != "" {
		h.Set("X-Msh-Device-Id", deviceID)
	}
}

// credentialData is what an account's credential keeps besides its tokens.
type credentialData struct {
	DeviceID string `json:"device_id"`
}

// upstream is one of Kimi's APIs.
type upstream struct {
	format, path string
}

var (
	upChat      = upstream{llm.FormatOpenAIChat, "/v1/chat/completions"}
	upResponses = upstream{llm.FormatOpenAIResponses, "/v1/responses"}
	// ?beta=true is how Anthropic's SDKs call Messages with betas; Kimi takes it the same way.
	upMessages = upstream{llm.FormatAnthropicMessages, "/v1/messages?beta=true"}
)

// upstreamFor returns the API that speaks the client's format: Responses and Messages clients
// get their own, every other format Chat Completions.
// bearer returns the token a credential sends to Kimi and its device data: a login's access token
// with the device ID made at login, or a Kimi Code API key, which has no device ID.
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
	body, tr, err := buildBody(call.Request, up)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+up.path, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
		if up == upMessages {
			// An uncompressed stream reaches the client event by event, as the Claude
			// extension asks Anthropic.
			req.Header.Set("Accept-Encoding", "identity")
		}
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if up == upMessages {
		// Kimi's Messages API is Anthropic's: the client's own Messages headers go with its
		// body, as the Claude extension sends them.
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
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		// A failed dial never reached the upstream, so trying again is safe.
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return upstreamError(resp, call.Request.Model)
	}
	dec := decoders[up.format]
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, dec.stream())
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, err := dec.response(raw)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

// reporting adds the translation report to the reply's response.start event.
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

var decoders = map[string]struct {
	stream   func() streamDecoder
	response func([]byte) ([]llm.Event, error)
}{
	llm.FormatOpenAIChat:        {func() streamDecoder { return openaichat.NewStreamDecoder() }, openaichat.DecodeResponse},
	llm.FormatOpenAIResponses:   {func() streamDecoder { return openairesponses.NewStreamDecoder() }, openairesponses.DecodeResponse},
	llm.FormatAnthropicMessages: {func() streamDecoder { return anthropicmessages.NewStreamDecoder() }, anthropicmessages.DecodeResponse},
}

// relayStream decodes the upstream stream and sends each event as soon as it is decoded. A
// blocked send blocks the next upstream read.
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

// upstreamError turns a non-2xx reply from Kimi (an OpenAI error body, or Anthropic's on the
// Messages API) into a structured error.
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
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeCredential, true
		if strings.Contains(e.ProviderCode, "quota") || strings.Contains(strings.ToLower(e.Message), "quota") {
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s == http.StatusServiceUnavailable || s == 529:
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// retryAfter reads a Retry-After header: delay seconds or an HTTP date.
func retryAfter(v string, now time.Time) int64 {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && secs >= 0 {
		return secs * 1000
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
}
