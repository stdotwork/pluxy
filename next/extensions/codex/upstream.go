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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

const codexOriginator = "codex-tui"

var codexUserAgent = codexUserAgentFor(codexClientVersion)

func codexUserAgentFor(version string) string {
	return "codex-tui/" + version + " (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; " + version + ")"
}

func (s settings) clientVersion() string {
	if s.CodexClientVersion == "" {
		return codexClientVersion
	}
	return s.CodexClientVersion
}

func (s settings) userAgent() string {
	if s.CodexClientVersion == "" {
		return codexUserAgent
	}
	return codexUserAgentFor(s.CodexClientVersion)
}

var passedHeaders = []string{
	"x-codex-beta-features", "version", "x-codex-turn-metadata", "x-codex-turn-state",
	"x-client-request-id", "x-codex-window-id", "thread-id", "session-id",
	"x-openai-internal-codex-responses-lite",
}

func upstreamTransport() *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}
}

var client = &http.Client{
	Transport: sdk.Upstream(upstreamTransport()),
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
	resp, err := post(ctx, call.Request, ur, ur.body, s, a)
	if err != nil {
		return err
	}
	if failed(resp) {
		e := statusError(resp, ur.model)
		resp.Body.Close()
		if !foreignReasoning(e) || call.Request.DropPolicy == llm.DropReject {
			return e
		}
		body, ok := ur.dropForeignReasoning()
		if !ok {
			return e
		}
		if resp, err = post(ctx, call.Request, ur, body, s, a); err != nil {
			return err
		}
		if failed(resp) {
			defer resp.Body.Close()
			return statusError(resp, ur.model)
		}
	}
	defer resp.Body.Close()
	headers := llm.ExtractRateLimitHeaders(llm.FormatOpenAIResponses, resp.Header)
	rep := ur.repairs()
	rep.req = &call.Request
	return relay(ctx, resp, renamer{&reporting{Stream: out, tr: ur.tr, headers: headers}, ur.names}, rep)
}

func post(ctx context.Context, cr llm.Request, ur *upstreamRequest, body []byte, s settings, a account) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(s, a)+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(req.Header, cr, ur, s, a)
	resp, err := client.Do(req)
	if err != nil {
		return nil, requestFailed(ctx, err)
	}
	return resp, nil
}

func failed(resp *http.Response) bool { return resp.StatusCode < 200 || resp.StatusCode > 299 }

func setHeaders(h http.Header, req llm.Request, ur *upstreamRequest, s settings, a account) {
	h.Set("Content-Type", "application/json")
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

func setSIWCHeaders(h http.Header, sessionID string, s settings) {
	if sessionID != "" {
		h["session_id"] = []string{sessionID}
		h.Set("X-Client-Request-Id", sessionID)
	}
	h.Set("User-Agent", siwcUserAgent(s))
}

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

func setIdentity(h http.Header, client map[string]string, s settings, a account) {
	switch {
	case s.identity():
		h.Set("User-Agent", s.userAgent())
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

type reporting struct {
	*sdk.Stream
	tr      *llm.Translation
	headers map[string]string
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		info.Headers = r.headers
		ev.Response = &info
	}
	return r.Stream.Send(ev)
}

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

func relay(ctx context.Context, resp *http.Response, out eventSender, rep *repairs) error {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
		}
		events, losses, err := openairesponses.DecodeResponseLosses(raw)
		for _, l := range llm.ReplyLosses(losses, rep.req.BodyReachesClient(llm.FormatOpenAIResponses, false)) {
			fmt.Fprintf(os.Stderr, "reply loss: %s %s (%s)\n", l.Kind, l.Path, l.Detail)
		}
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
	defer func() {
		for _, l := range llm.ReplyLosses(dec.Losses(), rep.req.BodyReachesClient(llm.FormatOpenAIResponses, true)) {
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

func emptyIncomplete() *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest,
		Message: "stream error: upstream terminated with incomplete empty response (0 tokens)"}
}

type repairs struct {
	done       map[int]json.RawMessage
	produced   bool
	keepOutput bool
	req        *llm.Request
}

func newRepairs() *repairs { return &repairs{done: map[int]json.RawMessage{}} }

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
	var r map[string]json.RawMessage
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

func hasID(v json.RawMessage) bool {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	return len(v) > 0
}

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

func statusError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ce := errorObject(raw)
	e := &llm.Error{
		Status:       resp.StatusCode,
		Message:      ce.Message,
		ProviderCode: ce.code(),
		Headers:      llm.ExtractRateLimitHeaders(llm.FormatOpenAIResponses, resp.Header),
	}
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
		e.Code, e.Scope, e.Retryable, e.Status = llm.CodeQuota, llm.ScopeCredential, true, http.StatusTooManyRequests
		e.RetryAfterMS = resetMS(ce, resp.Header.Get("Retry-After"))
		var windowReset int64
		e.QuotaWindowMinutes, windowReset = exhaustedWindow(resp.Header, time.Now())
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = windowReset
		}
	case capacityRefusal(raw):
		e.Code, e.Scope, e.Retryable, e.Status = llm.CodeRateLimit, llm.ScopeModel, true, http.StatusTooManyRequests
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 300 && s < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("upstream redirected (%s) to another host; not followed", resp.Status)
	case isAccountClosed(s, ce):
		e.Code, e.Scope, e.Retryable = llm.CodeAccountClosed, llm.ScopeCredential, true
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		if e.ProviderCode == "model_not_found" || (model != "" && strings.Contains(e.Message, model)) {
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
		e.RetryAfterMS = resetMS(ce, resp.Header.Get("Retry-After"))
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
	}
	return e
}

func failedFirst(events []llm.Event, err error) error {
	var e *llm.Error
	if errors.As(err, &e) && len(e.Raw) == 0 && len(events) > 0 && events[0].Type == llm.EventResponseStart {
		e.Raw = events[0].Raw
	}
	return err
}

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
		e.Code, e.Scope, e.Status = llm.CodeRateLimit, llm.ScopeModel, http.StatusTooManyRequests
	case code == "rate_limit_exceeded" || ce.Type == "rate_limit_error":
		e.Code, e.Scope, e.Status = llm.CodeRateLimit, llm.ScopeModel, http.StatusTooManyRequests
	case isAccountClosed(e.Status, ce):
		e.Code, e.Scope, e.Retryable = llm.CodeAccountClosed, llm.ScopeCredential, true
		if e.Status < 400 || e.Status > 599 {
			e.Status = http.StatusForbidden
		}
	case ce.Type == "invalid_request_error":
		e.Code, e.Scope, e.Status = llm.CodeInvalidRequest, llm.ScopeRequest, http.StatusBadRequest
	}
	return e
}

// The error object may be in "error", at the top of the body, or in
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
	_ = json.Unmarshal(raw, &b)
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

func resetMS(ce codexError, retryAfterHeader string) int64 {
	if ce.ResetsAt > 0 {
		if d := time.Until(time.Unix(ce.ResetsAt, 0)); d > 0 {
			return d.Milliseconds()
		}
	}
	if ce.ResetsInSeconds > 0 {
		return ce.ResetsInSeconds * 1000
	}
	return sdk.RetryAfter(retryAfterHeader, time.Now())
}

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
				reset = at + length*60
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

func isAccountClosed(status int, ce codexError) bool {
	code := strings.ToLower(strings.TrimSpace(ce.code()))
	typ := strings.ToLower(strings.TrimSpace(ce.Type))
	if code == "account_deactivated" || typ == "account_deactivated" {
		return true
	}
	if status != 0 && status != http.StatusBadRequest && status != http.StatusUnauthorized && status != http.StatusForbidden {
		return false
	}
	m := strings.ToLower(ce.Message)
	for _, s := range []string{
		"account_deactivated",
		"account has been deactivated",
		"this account is deactivated",
		"account has been disabled",
		"your account has been suspended",
		"organization has been disabled",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
