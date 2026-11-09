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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Codex client identity: the User-Agent and originator of the Codex terminal client, which
// today's core sends in place of the client's own (its "cloaking").
const (
	codexUserAgent  = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexOriginator = "codex-tui"
)

// passedHeaders are the Codex client's request headers sent on to the upstream as they came
// (lower-case, as the request's native headers name them); originator goes by the identity rule
// in setHeaders.
var passedHeaders = []string{
	"x-codex-beta-features", "version", "x-codex-turn-metadata", "x-codex-turn-state",
	"x-client-request-id", "x-codex-window-id", "thread-id", "session-id",
	"x-openai-internal-codex-responses-lite",
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, so the account's token never goes to another host.
var client = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	},
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	base := s.BaseURL
	if base == "" {
		base = defaultAPIBaseURL
		if a.subscription {
			base = defaultSubscriptionBaseURL
		}
	}
	ur, err := buildRequest(call.Request, call.Account.ID, s, a, false)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/responses", bytes.NewReader(ur.body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(req.Header, call.Request, ur, s, a)
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
		return statusError(resp, ur.model)
	}
	return relay(ctx, resp, &reporting{Stream: out, tr: ur.tr})
}

// setHeaders sets the upstream request's headers.
func setHeaders(h http.Header, req llm.Request, ur *upstreamRequest, s settings, a account) {
	h.Set("Content-Type", "application/json")
	// The Codex endpoint always streams.
	h.Set("Accept", "text/event-stream")
	h.Set("Authorization", "Bearer "+a.token)
	if ur.sessionID != "" {
		h.Set("Session-Id", ur.sessionID)
	}
	setClientHeaders(h, req, ur, s, a)
}

// setClientHeaders sets the headers an upstream request carries on HTTP and on the WebSocket
// handshake alike: the Codex client's own headers passed on, the client identity, and a
// subscription's account and routing hint.
func setClientHeaders(h http.Header, req llm.Request, ur *upstreamRequest, s settings, a account) {
	var client map[string]string
	if req.Native != nil {
		client = req.Native.Headers
	}
	for _, name := range passedHeaders {
		if v := client[name]; v != "" {
			h.Set(name, v)
		}
	}
	setIdentity(h, client, s, a)
	if a.subscription {
		hint := "model=" + ur.model
		var body struct {
			ServiceTier string `json:"service_tier"`
		}
		if json.Unmarshal(ur.body, &body) == nil && body.ServiceTier != "" {
			hint += ";tier=" + body.ServiceTier
		}
		h.Set("X-Codex-Routing-Hint", hint)
	}
}

// setIdentity sets the headers that name the Codex client: User-Agent and Originator (see
// settings.identity), and a subscription's ChatGPT account. client holds the Codex client's own
// request headers, when there is a request.
func setIdentity(h http.Header, client map[string]string, s settings, a account) {
	switch {
	case s.identity():
		h.Set("User-Agent", codexUserAgent)
		h.Set("Originator", codexOriginator)
	case client["originator"] != "":
		h.Set("Originator", client["originator"])
	case a.subscription:
		h.Set("Originator", codexOriginator)
	}
	if a.subscription && a.accountID != "" {
		h.Set("Chatgpt-Account-Id", a.accountID)
	}
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

type eventSender interface{ Send(llm.Event) error }

// relay decodes the upstream stream and sends each event as soon as it is decoded; a blocked
// send blocks the next upstream read. The Codex endpoint always streams, so a client that asked
// for a non-streamed reply gets the same events and core puts the reply together.
//
// Two repairs, as today's core makes them: a terminal Response whose output is empty gets the
// items the stream finished (response.output_item.done) as its output, and response.done, which
// some Codex streams end with, counts as response.completed.
func relay(ctx context.Context, resp *http.Response, out eventSender) error {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// The Codex endpoint answers with a stream; a JSON body is a whole Response.
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
		}
		events, err := openairesponses.DecodeResponse(raw)
		if err != nil {
			return classify(err)
		}
		for _, ev := range events {
			if err := out.Send(ev); err != nil {
				return err
			}
		}
		return nil
	}
	r := sse.NewReader(resp.Body)
	dec := openairesponses.NewStreamDecoder()
	done := map[int]json.RawMessage{} // output_index -> finished item
	for {
		e, err := r.Next()
		eof := err == io.EOF
		var events []llm.Event
		switch {
		case eof:
			events, err = dec.Close()
			if err != nil {
				return classify(err)
			}
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream broke: " + err.Error(), Scope: llm.ScopeEndpoint}
		default:
			events, err = dec.Decode(repair([]byte(e.Data), done))
			if err != nil {
				return classify(err)
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

// repair records finished output items and rewrites a terminal event as relay describes.
func repair(data []byte, done map[int]json.RawMessage) []byte {
	var ev map[string]json.RawMessage
	if json.Unmarshal(data, &ev) != nil {
		return data
	}
	var typ string
	_ = json.Unmarshal(ev["type"], &typ)
	switch typ {
	case "response.output_item.done":
		var idx int
		if json.Unmarshal(ev["output_index"], &idx) == nil {
			done[idx] = ev["item"]
		}
		return data
	case "response.done":
		ev["type"] = raw("response.completed")
	case "response.completed", "response.incomplete":
	default:
		return data
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(ev["response"], &r) == nil && r != nil && len(done) > 0 {
		var output []json.RawMessage
		_ = json.Unmarshal(r["output"], &output)
		if len(output) == 0 {
			idx := make([]int, 0, len(done))
			for i := range done {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				output = append(output, done[i])
			}
			r["output"] = raw(output)
			ev["response"] = raw(r)
		}
	}
	return raw(ev)
}

// codexError is the error object of a Codex refusal, in an HTTP error body or a stream's error
// or response.failed event.
type codexError struct {
	Type            string `json:"type"`
	Code            any    `json:"code"`
	Message         string `json:"message"`
	ResetsAt        int64  `json:"resets_at"`
	ResetsInSeconds int64  `json:"resets_in_seconds"`
}

func (c codexError) code() string {
	if s, ok := c.Code.(string); ok {
		return s
	}
	return ""
}

// errorObject finds the error object in a Codex error body: {"error": {...}}, a bare error
// object, or a response.failed event's response.error.
func errorObject(raw []byte) codexError {
	var b struct {
		codexError
		Error    *codexError `json:"error"`
		Response *struct {
			Error *codexError `json:"error"`
		} `json:"response"`
	}
	_ = json.Unmarshal(raw, &b)
	switch {
	case b.Error != nil:
		return *b.Error
	case b.Response != nil && b.Response.Error != nil:
		return *b.Response.Error
	}
	return b.codexError
}

// statusError turns a non-2xx upstream reply into a structured error.
func statusError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ce := errorObject(raw)
	e := &llm.Error{Status: resp.StatusCode, Message: ce.Message, ProviderCode: ce.code()}
	if e.ProviderCode == "" {
		e.ProviderCode = ce.Type
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("upstream returned %s", resp.Status)
	}
	if json.Valid(raw) {
		e.Raw = raw
	}
	switch s := resp.StatusCode; {
	case ce.Type == "usage_limit_reached":
		// The plan's usage allowance is used up until it resets.
		e.Code, e.Scope, e.Retryable, e.Status = llm.CodeQuota, llm.ScopeCredential, true, http.StatusTooManyRequests
		e.RetryAfterMS = resetMS(ce, resp.Header.Get("Retry-After"))
		e.QuotaWindowMinutes, _ = exhaustedWindow(resp.Header, time.Now())
	case s >= 300 && s < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("upstream redirected (%s) to another host; not followed", resp.Status)
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		if e.ProviderCode == "model_not_found" || (model != "" && strings.Contains(e.Message, model)) {
			// The upstream does not serve the model, so it did not run the request.
			e.Retryable = true
		}
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeCredential, true
		if e.ProviderCode == "insufficient_quota" {
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = resetMS(ce, resp.Header.Get("Retry-After"))
	case s == http.StatusServiceUnavailable:
		// Not retry-safe: a gateway may answer 503 after the model started on the request.
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"))
	case s >= 500:
		// The upstream may have run the request before failing.
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// classify gives a failure reported inside the stream (an error or response.failed event) the
// code, status and scope its Codex error type means; other errors pass through.
func classify(err error) error {
	var e *llm.Error
	if !errors.As(err, &e) || len(e.Raw) == 0 {
		return err
	}
	ce := errorObject(e.Raw)
	code := ce.code()
	switch {
	case ce.Type == "usage_limit_reached" || code == "usage_limit_reached":
		e.Code, e.Scope, e.Status, e.Retryable = llm.CodeQuota, llm.ScopeCredential, http.StatusTooManyRequests, true
		e.RetryAfterMS = resetMS(ce, "")
	case code == "context_length_exceeded" || ce.Type == "context_length_exceeded":
		e.Code, e.Scope, e.Status = llm.CodeInvalidRequest, llm.ScopeRequest, http.StatusBadRequest
	case code == "rate_limit_exceeded" || ce.Type == "rate_limit_error":
		e.Code, e.Scope, e.Status = llm.CodeRateLimit, llm.ScopeCredential, http.StatusTooManyRequests
	case ce.Type == "invalid_request_error":
		e.Code, e.Scope, e.Status = llm.CodeInvalidRequest, llm.ScopeRequest, http.StatusBadRequest
	}
	return e
}

// resetMS returns when a usage limit resets, in milliseconds from now: resets_at, else
// resets_in_seconds, else the Retry-After header.
func resetMS(ce codexError, retryAfterHeader string) int64 {
	if ce.ResetsAt > 0 {
		if d := time.Until(time.Unix(ce.ResetsAt, 0)); d > 0 {
			return d.Milliseconds()
		}
	}
	if ce.ResetsInSeconds > 0 {
		return ce.ResetsInSeconds * 1000
	}
	return retryAfter(retryAfterHeader)
}

// exhaustedWindow reads Codex's rate-limit headers (x-codex-primary-* and x-codex-secondary-*)
// on a usage_limit_reached reply: of the windows at 100% use, the one that resets last, since the
// account stays out until then. It returns that window's length in minutes, and when it resets
// in milliseconds from now if the headers say (reset-at in Unix seconds, or reset-after-seconds);
// 0 for what they do not say. Today's core reads them the same way.
func exhaustedWindow(h http.Header, now time.Time) (minutes, resetMS int64) {
	num := func(name string) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(h.Get(name)), 64)
		return v, err == nil
	}
	at := float64(now.UnixMilli()) / 1000
	var best, bestReset float64
	var explicit bool
	for _, slot := range []string{"primary", "secondary"} {
		prefix := "X-Codex-" + slot + "-"
		used, okUsed := num(prefix + "used-percent")
		length, okLength := num(prefix + "window-minutes")
		if !okUsed || used < 100 || !okLength || length <= 0 {
			continue
		}
		reset, told := num(prefix + "reset-at")
		if !told {
			var after float64
			if after, told = num(prefix + "reset-after-seconds"); told {
				reset = at + after
			} else {
				reset = at + length*60 // without a reset time, the longer window ends later
			}
		}
		if best == 0 || reset > bestReset {
			best, bestReset, explicit = length, reset, told
		}
	}
	if explicit && bestReset > at {
		resetMS = int64((bestReset - at) * 1000)
	}
	return int64(best), resetMS
}

// retryAfter reads a Retry-After header: delay seconds or an HTTP date.
func retryAfter(v string) int64 {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && secs >= 0 {
		return secs * 1000
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
}
