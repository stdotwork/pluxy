// Command xai is pluxy's provider extension for xAI's Grok models, with an xAI API key.
//
// Account settings (optional):
//
//	{"base_url": "https://api.x.ai/v1"}
//
// base_url is where xAI's API lives; requests go to {base_url}/responses. Every client format is
// sent to xAI's Responses API: a Responses client's body passes through with the changes xAI needs
// (body.go), and the other formats are translated to Responses. The account's credential must be
// of kind api_key; it is sent as a bearer token.
//
// The extension names itself in its User-Agent (pluxy-xai) and sends no other product's identity.
// Grok CLI's OAuth login and its cli-chat-proxy endpoint are not built.
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
	id      = "pluxy/xai"
	version = "0.1.0"
)

// defaultBaseURL is xAI's API.
const defaultBaseURL = "https://api.x.ai/v1"

// freeUsageCooldownMS is how long an account is held back when xAI reports its free usage used up
// (the 24-hour cooldown today's core applies).
const freeUsageCooldownMS = 24 * 60 * 60 * 1000

func main() {
	err := sdk.Serve(sdk.Extension{ID: id, Version: version, Generate: generate})
	if err != nil {
		fmt.Fprintln(os.Stderr, "xai:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL string `json:"base_url"`
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
	return s, nil
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, so the key is not sent elsewhere.
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

func apiKey(c *protocol.Credential) (string, error) {
	if c != nil && c.Kind == protocol.CredentialAPIKey && c.APIKey != "" {
		return c.APIKey, nil
	}
	return "", &llm.Error{Code: llm.CodeAuth, Message: "the account has no xAI API key", Scope: llm.ScopeCredential}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	key, err := apiKey(call.Account.Credential)
	if err != nil {
		return err
	}

	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr}
	url := s.BaseURL + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "pluxy-xai/"+version)
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// The conversation's ID is the prompt cache key the body carries (the Responses client's own).
	// Today's core derives one when the client sends none; this extension does not.
	if conv := promptCacheKey(body); conv != "" {
		req.Header.Set("x-grok-conv-id", conv)
	}

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
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, openairesponses.NewStreamDecoder())
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

// promptCacheKey returns the body's prompt_cache_key, trimmed, or "".
func promptCacheKey(body []byte) string {
	var m struct {
		Key string `json:"prompt_cache_key"`
	}
	_ = json.Unmarshal(body, &m)
	return strings.TrimSpace(m.Key)
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

// upstreamError turns a non-2xx reply from xAI into a structured error. xAI's bodies name the
// error in error.message and error.code, or with a top-level code and a string error.
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
		// An invalid key is an authentication failure, not a permission one.
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeCredential, true
		if freeUsageUsedUp(lower) {
			// Free usage is used up for a day; the account is held back that long.
			e.Code, e.RetryAfterMS = llm.CodeQuota, freeUsageCooldownMS
			break
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

// badCredentials reports whether an error body (lower-cased) says the key is invalid. Today's core
// checks the same two markers, on a 403.
func badCredentials(lower string) bool {
	return strings.Contains(lower, "bad-credentials") || strings.Contains(lower, "access token could not be validated")
}

// freeUsageUsedUp reports whether a 429 body (lower-cased) says xAI's free usage is used up, the
// markers today's core uses.
func freeUsageUsedUp(lower string) bool {
	return strings.Contains(lower, "free-usage-exhausted") || strings.Contains(lower, "included free usage")
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
