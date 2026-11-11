// Command meta is pluxy's provider extension for Meta's Muse models on api.meta.ai: a Muse Code
// subscription signed in with Meta's device-code login (login.go), or a Meta API key.
//
// Account settings (all optional):
//
//	{"base_url": "https://api.meta.ai/v1"}
//
// base_url is where Meta's API lives; requests go to {base_url}/responses. Without it a login's
// requests go where Meta said when the key was made, and a key's to https://api.meta.ai/v1.
// Tests move Meta's login and its key endpoint to a fake with auth_base_url and mint_url
// (sdk.TestLoginHost); in a normal run they are ignored.
//
// Every client format is sent to Meta's Responses API, always as a stream, as Muse Code sends it
// and today's core does: a Responses client's body passes through with the changes Meta needs
// (body.go), and the other formats are translated to Responses. Requests carry Muse Code's client
// identity (identity.go).
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
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/meta"
	version = "0.1.0"
)

// defaultBaseURL is Meta's API.
const defaultBaseURL = "https://api.meta.ai/v1"

// notFoundCooldownMS is how long an account is held back from a model Meta says it does not have,
// when Meta names no reset time (today's core holds it five minutes).
const notFoundCooldownMS = 5 * 60 * 1000

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
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "meta:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL     string `json:"base_url"`
	AuthBaseURL string `json:"auth_base_url"`
	MintURL     string `json:"mint_url"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	s.AuthBaseURL = strings.TrimRight(sdk.TestLoginHost(s.AuthBaseURL), "/")
	if s.AuthBaseURL == "" {
		s.AuthBaseURL = defaultAuthBaseURL
	}
	if s.MintURL = sdk.TestLoginHost(s.MintURL); s.MintURL == "" {
		s.MintURL = defaultMintURL
	}
	return s, nil
}

// credentialData is what a login's credential keeps besides its tokens.
type credentialData struct {
	// BaseURL is where Meta said the minted key's API is.
	BaseURL string `json:"base_url,omitempty"`
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, so the key is not sent elsewhere.
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

// target returns the key a credential sends to Meta and the API it goes to: a login's minted key
// (its access token), or a Meta API key. A device-login token (dca:...) is not a key Meta's API
// takes.
func target(s settings, c *protocol.Credential) (key, baseURL string, err error) {
	baseURL = s.BaseURL
	switch {
	case c == nil:
	case c.Kind == protocol.CredentialOAuth && c.AccessToken != "" && !strings.HasPrefix(c.AccessToken, "dca:"):
		key = c.AccessToken
		var d credentialData
		_ = json.Unmarshal(c.Data, &d)
		if baseURL == "" {
			baseURL = strings.TrimRight(d.BaseURL, "/")
		}
	case c.Kind == protocol.CredentialAPIKey && c.APIKey != "" && !strings.HasPrefix(c.APIKey, "dca:"):
		key = c.APIKey
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if key == "" {
		return "", "", &llm.Error{Code: llm.CodeAuth, Message: "the account has no Meta API key (a dca: login token is not one; log in again)", Scope: llm.ScopeCredential}
	}
	return key, baseURL, nil
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	key, baseURL, err := target(s, call.Account.Credential)
	if err != nil {
		return err
	}
	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	clientIdentity(req.Header)

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
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return upstreamError(resp.StatusCode, raw, resp.Header, call.Request.Model, time.Now())
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, call.Request.Model)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, err := openairesponses.DecodeResponse(raw)
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

// relayStream decodes Meta's Responses stream and sends each event as soon as it is decoded. A
// blocked send blocks the next upstream read. An error event ends the request with Meta's error.
// Meta's response.subscription_usage events, which are not part of the Responses stream, are
// ignored by the decoder like any event it does not know.
func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }, model string) error {
	r := sse.NewReader(body)
	dec := openairesponses.NewStreamDecoder()
	sent := false
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
			var head struct {
				Type     string `json:"type"`
				Response struct {
					Error json.RawMessage `json:"error"`
				} `json:"response"`
			}
			_ = json.Unmarshal([]byte(e.Data), &head)
			failure := []byte(nil)
			switch {
			case head.Type == "error":
				failure = []byte(e.Data)
			case head.Type == "response.failed" && len(head.Response.Error) > 0 && string(head.Response.Error) != "null":
				failure, _ = json.Marshal(map[string]json.RawMessage{"error": head.Response.Error})
			}
			if failure != nil {
				fe := streamError(failure, model, dec.Usage())
				// Once the reply began, the model ran: trying again is not safe.
				fe.Retryable = fe.Retryable && !sent
				return fe
			}
			events, err = dec.Decode([]byte(e.Data))
			if err != nil {
				return err
			}
		}
		for _, ev := range events {
			if err := out.Send(ev); err != nil {
				return err
			}
			sent = true
			if ev.Terminal() {
				return nil
			}
		}
		if eof {
			return nil
		}
	}
}

// streamError turns an error event of the stream into a structured error, with the status the
// event names (a 502 when it names none).
func streamError(data []byte, model string, usage *llm.Usage) *llm.Error {
	var ev struct {
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &ev)
	status := http.StatusBadGateway
	switch c := ev.Error.Code.(type) {
	case float64:
		if c >= 400 && c <= 599 {
			status = int(c)
		}
	case string:
		// Meta also names the status as a string ("401").
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && n >= 400 && n <= 599 {
			status = n
		}
	}
	e := upstreamError(status, data, nil, model, time.Now())
	e.Usage = usage
	return e
}

// metaError is the error body Meta's API answers with: an OpenAI-style error object, or a string
// error with a top-level code.
type metaError struct {
	Error struct {
		Message  string `json:"message"`
		Type     string `json:"type"`
		Code     any    `json:"code"`
		ResetsAt int64  `json:"resets_at"`
	}
}

func parseError(raw []byte) metaError {
	var top struct {
		Code  any             `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	var p metaError
	_ = json.Unmarshal(raw, &top)
	if json.Unmarshal(top.Error, &p.Error) != nil {
		_ = json.Unmarshal(top.Error, &p.Error.Message)
	}
	if p.Error.Code == nil {
		p.Error.Code = top.Code
	}
	return p
}

// upstreamError turns a non-2xx reply from Meta into a structured error.
func upstreamError(status int, raw []byte, h http.Header, model string, now time.Time) *llm.Error {
	parsed := parseError(raw)
	code := ""
	if parsed.Error.Code != nil {
		code = fmt.Sprint(parsed.Error.Code)
	}
	e := &llm.Error{Status: status, Message: parsed.Error.Message, ProviderCode: parsed.Error.Type}
	if e.ProviderCode == "" {
		e.ProviderCode = code
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("upstream returned %d %s", status, http.StatusText(status))
	}
	if json.Valid(raw) {
		e.Raw = raw
	}
	resetMS := int64(0)
	if parsed.Error.ResetsAt > 0 {
		if d := time.Unix(parsed.Error.ResetsAt, 0).Sub(now); d > 0 {
			resetMS = d.Milliseconds()
		}
	}
	switch {
	case status >= 300 && status < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("upstream redirected (%d) to another host; not followed", status)
	case status == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case status == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case status == http.StatusNotFound:
		// Today's core holds the account back from the model until Meta's reset time, or five
		// minutes.
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		e.Retryable = model != "" && strings.Contains(e.Message, model)
		e.RetryAfterMS = resetMS
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = notFoundCooldownMS
		}
	case status == http.StatusTooManyRequests:
		// Only the model waits on this account, as today's core cools only the model that
		// answered; a used-up subscription quota is the account's.
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if h != nil {
			e.RetryAfterMS = retryAfter(h.Get("Retry-After"), now)
		}
		if resetMS > 0 {
			e.RetryAfterMS = resetMS
		}
		if subscriptionQuota(parsed, code) {
			e.Code, e.Scope = llm.CodeQuota, llm.ScopeCredential
		}
	case status == http.StatusServiceUnavailable || status == 529:
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		if h != nil {
			e.RetryAfterMS = retryAfter(h.Get("Retry-After"), now)
		}
	case status >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// subscriptionQuota reports whether a 429 says the subscription's quota is used up rather than
// that the account is sending too fast, as today's core decides.
func subscriptionQuota(p metaError, code string) bool {
	msg, code := strings.ToLower(p.Error.Message), strings.ToLower(code)
	if strings.Contains(msg, "subscription quota") || strings.Contains(msg, "quota exhausted") {
		return true
	}
	return (code == "rate_limit_exceeded" || strings.Contains(code, "quota")) && p.Error.ResetsAt > 0
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
