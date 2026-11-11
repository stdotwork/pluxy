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
	Transport: sdk.Upstream(&http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}),
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
	if a.siwc && codexClient(call.Request) {
		return siwcCodexClient()
	}
	ur, err := buildRequest(call.Request, call.Account.ID, s, a, false)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+"/responses", bytes.NewReader(ur.body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(req.Header, call.Request, ur, s, a)
	resp, err := client.Do(req)
	if err != nil {
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp, ur.model)
	}
	return relay(ctx, resp, renamer{&reporting{Stream: out, tr: ur.tr}, ur.names}, ur.repairs())
}

// setHeaders sets the upstream request's headers.
func setHeaders(h http.Header, req llm.Request, ur *upstreamRequest, s settings, a account) {
	h.Set("Content-Type", "application/json")
	// The Codex endpoint always streams.
	h.Set("Accept", "text/event-stream")
	h.Set("Authorization", "Bearer "+a.token)
	if a.siwc {
		setSIWCHeaders(h, ur.sessionID, s)
		return
	}
	if ur.sessionID != "" {
		h.Set("Session-Id", ur.sessionID)
	}
	setClientHeaders(h, req, ur, s, a)
}

// setSIWCHeaders sets a SIWC account's own headers, as today's core sends them: the session as
// session_id (that spelling) and as X-Client-Request-Id, and the SIWC User-Agent. None of the
// Codex client's headers, the Codex identity, a ChatGPT account or a routing hint go to the
// OpenAI API.
func setSIWCHeaders(h http.Header, sessionID string, s settings) {
	if sessionID != "" {
		h["session_id"] = []string{sessionID}
		h.Set("X-Client-Request-Id", sessionID)
	}
	h.Set("User-Agent", siwcUserAgent(s))
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

// renamer gives a tool call the client's own name for the function, where the request sent the
// upstream another (see shaper.limits).
type renamer struct {
	eventSender
	names map[string]string
}

func (r renamer) Send(ev llm.Event) error {
	if ev.Type == llm.EventItemStart && ev.Item != nil && r.names[ev.Item.Name] != "" {
		item := *ev.Item
		item.Name = r.names[item.Name]
		ev.Item = &item
	}
	return r.eventSender.Send(ev)
}

type eventSender interface{ Send(llm.Event) error }

// relay decodes the upstream stream and sends each event as soon as it is decoded; a blocked
// send blocks the next upstream read. The Codex endpoint always streams, so a client that asked
// for a non-streamed reply gets the same events and core puts the reply together.
//
// Four repairs, as today's core makes them: a terminal Response whose output is empty gets the
// items the stream finished (response.output_item.done) as its output; an output item without an
// id gets the id of the finished item at its position; response.done, which some Codex streams
// end with, counts as response.completed; and a response.incomplete that produced nothing is an
// error (see repairs.apply). rep says which of them a Responses-lite client's reply skips.
func relay(ctx context.Context, resp *http.Response, out eventSender, rep *repairs) error {
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
			var data []byte
			if data, err = rep.apply([]byte(e.Data)); err != nil {
				return err
			}
			events, err = dec.Decode(data)
			if err != nil {
				return classify(failedFirst(events, err))
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

// emptyIncomplete is the error for a reply that ended with response.incomplete without having
// produced anything. The upstream may have run the request, so it is not retried; the scope is the
// request, as it says nothing about the account, so routing puts no cooldown on the account and
// does not move the request to another one (today's core's request-scoped error).
func emptyIncomplete() *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest,
		Message: "stream error: upstream terminated with incomplete empty response (0 tokens)"}
}

// repairs holds what the repairs of one reply need from the events before its terminal event.
type repairs struct {
	done     map[int]json.RawMessage // output_index -> finished item
	produced bool                    // a delta with text, or a finished item, came
	// keepOutput leaves the terminal event's output as it came, ids and emptiness included: a
	// Responses-lite client's reply, as today's core sends it.
	keepOutput bool
}

func newRepairs() *repairs { return &repairs{done: map[int]json.RawMessage{}} }

// apply records finished output items and rewrites a terminal event as relay describes. It
// fails for a response.incomplete that produced nothing: no output delta with text, no finished
// item, no output in the event, and a usage whose output_tokens is exactly the number 0. That is
// an upstream that gave up silently, which today's core reports as an error rather than as a
// reply; a 0 that is missing, null, a string or written 0.0 does not show it.
func (p *repairs) apply(data []byte) ([]byte, error) {
	var ev map[string]json.RawMessage
	if json.Unmarshal(data, &ev) != nil {
		return data, nil
	}
	var typ string
	_ = json.Unmarshal(ev["type"], &typ)
	switch typ {
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
		var delta string
		if json.Unmarshal(ev["delta"], &delta) == nil && strings.TrimSpace(delta) != "" {
			p.produced = true
		}
		return data, nil
	case "response.output_item.done":
		var item map[string]json.RawMessage
		if json.Unmarshal(ev["item"], &item) == nil && item != nil {
			p.produced = true
		}
		var idx int
		if json.Unmarshal(ev["output_index"], &idx) == nil {
			p.done[idx] = ev["item"]
		}
		return data, nil
	case "response.done":
		ev["type"] = raw("response.completed")
	case "response.completed", "response.incomplete":
	default:
		return data, nil
	}
	var r map[string]json.RawMessage // nil when the event has no response object
	_ = json.Unmarshal(ev["response"], &r)
	var output []json.RawMessage
	_ = json.Unmarshal(r["output"], &output)
	if typ == "response.incomplete" && !p.produced && len(output) == 0 && r != nil {
		var usage map[string]json.RawMessage
		if json.Unmarshal(r["usage"], &usage) == nil && string(bytes.TrimSpace(usage["output_tokens"])) == "0" {
			return nil, emptyIncomplete()
		}
	}
	if r != nil && len(p.done) > 0 {
		if len(output) == 0 {
			if p.keepOutput {
				return raw(ev), nil
			}
			idx := make([]int, 0, len(p.done))
			for i := range p.done {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				output = append(output, p.done[i])
			}
			r["output"] = raw(output)
			ev["response"] = raw(r)
		} else if !p.keepOutput && p.restoreIDs(output) {
			r["output"] = raw(output)
			ev["response"] = raw(r)
		}
	}
	return raw(ev), nil
}

// restoreIDs gives each output item that has no id (missing, null or blank) the id of the
// finished item at the same position, when that has one, as today's core does. It reports whether
// it changed any.
func (p *repairs) restoreIDs(output []json.RawMessage) bool {
	changed := false
	for i, it := range output {
		var item map[string]json.RawMessage
		if json.Unmarshal(it, &item) != nil || item == nil || hasID(item["id"]) {
			continue
		}
		var finished struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(p.done[i], &finished) != nil || strings.TrimSpace(finished.ID) == "" {
			continue
		}
		item["id"] = raw(finished.ID)
		output[i] = raw(item)
		changed = true
	}
	return changed
}

// hasID reports whether an item's id is there: any value but null and a blank string.
func hasID(v json.RawMessage) bool {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	return len(v) > 0
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
		var windowReset int64
		e.QuotaWindowMinutes, windowReset = exhaustedWindow(resp.Header, time.Now())
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = windowReset
		}
	case capacityRefusal(raw):
		// The turn or the model is full, not the account's allowance: refused before it ran, so
		// another account may take it, and only this model waits on this account. Whatever the
		// upstream's status, the client sees a 429, as from today's core.
		e.Code, e.Scope, e.Retryable, e.Status = llm.CodeRateLimit, llm.ScopeModel, true, http.StatusTooManyRequests
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"))
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
		// Only the model waits on this account, as today's core cools only the model that answered;
		// a used-up plan allowance (usage_limit_reached, above) is the account's.
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			// A call for no model (the usage endpoint): the account is the one limited.
			e.Scope = llm.ScopeCredential
		}
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

// failedFirst puts back on err the event a stream failed with when it was the stream's first
// event: the decoder then moves that event's raw onto the response.start it opens the reply with,
// which is never sent because the stream failed, and classify reads the failure from the raw.
func failedFirst(events []llm.Event, err error) error {
	var e *llm.Error
	if errors.As(err, &e) && len(e.Raw) == 0 && len(events) > 0 && events[0].Type == llm.EventResponseStart {
		e.Raw = events[0].Raw
	}
	return err
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
	case capacityRefusal(e.Raw):
		// Not retryable: the stream is open, and today's core does not move such a turn to
		// another account unless its opt-in bootstrap buffering is on.
		e.Code, e.Scope, e.Status = llm.CodeRateLimit, llm.ScopeModel, http.StatusTooManyRequests
	case code == "rate_limit_exceeded" || ce.Type == "rate_limit_error":
		e.Code, e.Scope, e.Status = llm.CodeRateLimit, llm.ScopeModel, http.StatusTooManyRequests
	case ce.Type == "invalid_request_error":
		e.Code, e.Scope, e.Status = llm.CodeInvalidRequest, llm.ScopeRequest, http.StatusBadRequest
	}
	return e
}

// capacityRefusal reports whether a Codex refusal says the turn or the model is full while the
// account's allowance is not used up, as today's core reads it. Three forms count:
//   - the code turn_rate_limit_exceeded (Codex's per-turn admission limit);
//   - the codes model_at_capacity and model_is_at_capacity;
//   - with no code at all, a message that says "at capacity" and either comes with the error type
//     rate_limit_error or names the model ("Selected model is at capacity"), or names one of the
//     model codes.
//
// Another explicit code keeps its own handling, so a plain rate_limit_exceeded is never taken for
// capacity by its message. The error object may be in "error", at the top of the body, or in
// "body.error" (a WebSocket message) or "response.error" (a response.failed event), searched in
// that order; the first code found decides.
func capacityRefusal(raw []byte) bool {
	var b struct {
		codexError
		Error *codexError `json:"error"`
		Body  *struct {
			Error *codexError `json:"error"`
		} `json:"body"`
		Response *struct {
			Error *codexError `json:"error"`
		} `json:"response"`
	}
	_ = json.Unmarshal(raw, &b) // a field of the wrong type is left out; the rest is still read
	var objs []codexError
	if b.Error != nil {
		objs = append(objs, *b.Error)
	}
	objs = append(objs, b.codexError)
	if b.Body != nil && b.Body.Error != nil {
		objs = append(objs, *b.Body.Error)
	}
	if b.Response != nil && b.Response.Error != nil {
		objs = append(objs, *b.Response.Error)
	}
	var rateLimitTyped, atCapacity, namesModel, namesModelCode bool
	for _, o := range objs {
		switch strings.ToLower(strings.TrimSpace(o.code())) {
		case "":
		case "turn_rate_limit_exceeded", "model_at_capacity", "model_is_at_capacity":
			return true
		default:
			return false
		}
		rateLimitTyped = rateLimitTyped || strings.EqualFold(strings.TrimSpace(o.Type), "rate_limit_error")
		msg := strings.ToLower(o.Message)
		atCapacity = atCapacity || strings.Contains(msg, "at capacity")
		namesModel = namesModel || strings.Contains(msg, "model")
		namesModelCode = namesModelCode || strings.Contains(msg, "model_at_capacity") || strings.Contains(msg, "model_is_at_capacity")
	}
	return namesModelCode || atCapacity && (rateLimitTyped || namesModel)
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
