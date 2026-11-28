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

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/gemini"
	version = "0.1.0"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     masked(generate),
		LoginMethods: loginMethods,
		Login:        login,
		CountTokens:  masked(countTokens),
		Models:       listModels,
		Check:        check,
		Quota:        quota,
		Endpoint:     masked(endpoint),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "gemini:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL      string          `json:"base_url"`
	Vertex       *vertexSettings `json:"vertex"`
	Interactions bool            `json:"interactions"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	if s.Vertex != nil {
		return s, s.Vertex.check()
	}
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL
	}
	return s, nil
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, since Go keeps x-goog-api-key on a redirect to another host.
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

func method(stream bool) string {
	if stream {
		return ":streamGenerateContent?alt=sse"
	}
	return ":generateContent"
}

type target struct {
	url       string
	authorize func(http.Header)
	token     string
	sa        *serviceAccount
}

func resolve(ctx context.Context, s settings, cred *protocol.Credential, model string, stream bool) (target, error) {
	return resolveMethod(ctx, s, cred, model, method(stream))
}

func resolveMethod(ctx context.Context, s settings, cred *protocol.Credential, model, m string) (target, error) {
	if s.Vertex != nil {
		return s.Vertex.resolve(ctx, s.BaseURL, cred, model, m)
	}
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the account has no API key", Scope: llm.ScopeCredential}
	}
	key := cred.APIKey
	return target{
		url:       s.BaseURL + "/v1beta/models/" + url.PathEscape(model) + m,
		authorize: func(h http.Header) { h.Set("X-Goog-Api-Key", key) },
	}, nil
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	return generateTo(ctx, call, out)
}

type sender interface{ Send(llm.Event) error }

func generateTo(ctx context.Context, call sdk.Call, out sender) (err error) {
	ctx = withAccountID(ctx, call.Account.ID)
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBodyOn(call.Request, s)
	if err != nil {
		return err
	}
	body = cleanToolSchemas(body)
	body = fixTurns(body, s.Vertex != nil, call.Request.Stream, !tr.Passthrough)
	send := &reporting{out: out, tr: tr, names: replyNames(call.Request, tr), calls: openairesponses.NewCustomCallCheck(call.Request, tr)}
	if !tr.Passthrough {
		// Gemini's function arguments are a protobuf Struct, so an integer parameter comes
		// back as a double; the request's schemas say which ones are integers.
		send.schemas = toolSchemas(call.Request)
		send.callNames = map[callKey]string{}
	}
	t, err := resolve(ctx, s, call.Account.Credential, call.Request.Model, call.Request.Stream)
	if err != nil {
		return err
	}
	if t.token != "" {
		send.redact = protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.token})
		defer func() {
			if e, ok := err.(*llm.Error); ok {
				err = send.redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}
	if s.Vertex != nil && !vertexCallIDs(tr.From) {
		body = stripCallIDs(body)
	}
	for first := true; ; first = false {
		resp, err := postBody(ctx, t, body, call.Request.Stream, call.Request.Model)
		if err != nil {
			return err
		}
		again, err := relayReply(ctx, resp, send, first, &call.Request)
		if again {
			continue
		}
		var e *llm.Error
		if !first && geminigenerate.IsMalformedCall(err) && errors.As(err, &e) {
			e.Message += "; it was asked for again once, and that reply ended the same way"
		}
		if err != nil && errors.As(err, &e) && len(e.Raw) > 0 &&
			(e.Status == http.StatusTooManyRequests || e.Code == llm.CodeRateLimit || e.Code == llm.CodeQuota) {
			observeRateLimit(ctx, nil, e.Raw, nil, false, call.Request.Model, send.redact, time.Now())
		}
		return err
	}
}

func relayReply(ctx context.Context, resp *http.Response, send sender, mayRetry bool, req *llm.Request) (again bool, err error) {
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if !mayRetry {
			return false, relayStream(ctx, resp.Body, send, req.BodyReachesClient(llm.FormatGeminiGenerate, true))
		}
		h := &holding{out: send}
		err := relayStream(ctx, resp.Body, h, req.BodyReachesClient(llm.FormatGeminiGenerate, true))
		if !h.open {
			if geminigenerate.IsMalformedCall(err) {
				return true, nil
			}
			if ferr := h.flush(); ferr != nil {
				return false, ferr
			}
		}
		return false, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, losses, err := geminigenerate.DecodeResponseLosses(raw)
	for _, l := range llm.ReplyLosses(losses, req.BodyReachesClient(llm.FormatGeminiGenerate, false)) {
		fmt.Fprintf(os.Stderr, "reply loss: %s %s (%s)\n", l.Kind, l.Path, l.Detail)
	}
	if err != nil {
		return mayRetry && geminigenerate.IsMalformedCall(err), llm.MarkContextOverflow(err)
	}
	for _, ev := range events {
		if err := send.Send(ev); err != nil {
			return false, err
		}
	}
	return false, nil
}

type holding struct {
	out  sender
	held []llm.Event
	open bool
}

func (h *holding) Send(ev llm.Event) error {
	if h.open {
		return h.out.Send(ev)
	}
	h.held = append(h.held, ev)
	if ev.Type == llm.EventResponseStart || ev.Type == llm.EventNative {
		return nil
	}
	return h.flush()
}

func (h *holding) flush() error {
	h.open = true
	held := h.held
	h.held = nil
	for _, ev := range held {
		if err := h.out.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func postBody(ctx context.Context, t target, body []byte, stream bool, model string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	t.authorize(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		var redact *protocol.Redactor
		if resp.StatusCode == http.StatusTooManyRequests && t.token != "" {
			redact = protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.token})
		}
		e := upstreamErrorFor(ctx, resp, model, redact)
		if t.sa != nil && resp.StatusCode == http.StatusUnauthorized {
			tokens.invalidate(t.sa, t.token)
		}
		return nil, e
	}
	return resp, nil
}

func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	return buildBodyOn(req, settings{BaseURL: defaultBaseURL})
}

// takesServiceTier reports whether the account's upstream is known to take a service tier in its
// GenerateContent request: the Gemini API at its own host does (the GenerateContentRequest
// reference), Vertex AI's request has none, and any other host may refuse a field it does not list.
func takesServiceTier(s settings) bool {
	if s.Vertex != nil {
		return false
	}
	u, err := url.Parse(s.BaseURL)
	return err == nil && strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com")
}

func takesWebSearch(s settings) bool {
	if s.Vertex != nil {
		if s.BaseURL == "" {
			return true
		}
		u, err := url.Parse(s.BaseURL)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
			return false
		}
		return isVertexHost(u.Hostname())
	}
	u, err := url.Parse(s.BaseURL)
	return err == nil && strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com")
}

// takesCandidateCount reports whether the account's upstream is documented to take
// generationConfig.candidateCount > 1 for this request: the Gemini API at
// generativelanguage.googleapis.com takes it for both streaming and non-streaming; Vertex AI
// documents multiple candidates only for non-streaming generateContent (streamGenerateContent is
// not supported). Any other host returns false.
func takesCandidateCount(s settings, stream bool) bool {
	if s.Vertex != nil {
		return !stream && takesWebSearch(s)
	}
	return takesServiceTier(s)
}

func isVertexHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "aiplatform.googleapis.com" || h == "aiplatform.us.rep.googleapis.com" || h == "aiplatform.eu.rep.googleapis.com" {
		return true
	}
	if loc, ok := strings.CutSuffix(h, "-aiplatform.googleapis.com"); ok {
		return loc != "" && !strings.HasPrefix(loc, "-") && !strings.HasSuffix(loc, "-") && locationPattern.MatchString(loc)
	}
	return false
}

func buildBodyOn(req llm.Request, s settings) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: llm.FormatGeminiGenerate}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == llm.FormatGeminiGenerate {
			tr.Passthrough = true
			body := n.Body
			if s := req.Options.ThinkingSuffix; s != "" {
				var losses []llm.Loss
				var err error
				if body, losses, _, err = geminigenerate.ApplyEffortReport(body, req.Model, s); err != nil {
					return nil, nil, err
				}
				for _, l := range losses {
					tr.Losses = llm.AddLoss(tr.Losses, l)
				}
			}
			return body, tr, nil
		}
	}
	var extra []llm.Loss
	if tr.From == llm.FormatAnthropicMessages && req.Options.MaxOutputTokens != nil {
		req.Options.MaxOutputTokens = nil
		extra = append(extra, llm.Loss{Kind: llm.LossDropped, Path: "max_tokens", Detail: "a Messages client's required max_tokens is left to the Gemini model's own limit"})
	}
	body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{
		ServiceTier:    takesServiceTier(s),
		WebSearch:      takesWebSearch(s),
		CodeExecution:  takesWebSearch(s),
		Vertex:         s.Vertex != nil,
		CandidateCount: takesCandidateCount(s, req.Stream),
	})
	if err != nil {
		return nil, nil, err
	}
	losses = append(extra, losses...)
	effort := req.Options.ReasoningEffort
	if s := req.Options.ThinkingSuffix; s != "" {
		effort = s
	}
	var more []llm.Loss
	if effort != "" {
		var applied bool
		body, more, applied, err = geminigenerate.ApplyEffortReport(body, req.Model, effort)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			losses = dropLoss(losses, "reasoning_effort")
		}
	}
	var summary []llm.Loss
	var carried bool
	if body, summary, carried, err = geminigenerate.ApplySummary(body, req.Model, req); err != nil {
		return nil, nil, err
	}
	if carried && req.Origin != nil {
		switch req.Origin.Format {
		case llm.FormatAnthropicMessages:
			losses = dropLoss(losses, "thinking")
		case llm.FormatOpenAIResponses:
			losses = dropLoss(losses, "reasoning")
		}
	}
	for _, l := range slices.Concat(more, summary) {
		losses = llm.AddLoss(losses, l)
	}
	tr.Losses = losses
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		paths := tr.DroppedPaths()
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
			Message: "Gemini GenerateContent cannot carry parts of this request: " + strings.Join(paths, ", ")}
	}
	return body, tr, nil
}

func dropLoss(losses []llm.Loss, path string) []llm.Loss {
	out := losses[:0]
	for _, l := range losses {
		if l.Path != path {
			out = append(out, l)
		}
	}
	return out
}

type reporting struct {
	out       sender
	tr        *llm.Translation
	redact    *protocol.Redactor
	known     []string
	names     map[string]string
	schemas   map[string]json.RawMessage
	callNames map[callKey]string
	calls     *openairesponses.CustomCallCheck
}

type callKey struct{ candidate, item int }

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		ev.Response = &info
	}
	if ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
		name := ev.Item.Name
		if client, ok := r.names[name]; ok {
			name = client
			item := *ev.Item
			item.Name = name
			ev.Item = &item
		}
		if r.callNames != nil {
			r.callNames[callKey{0, ev.Index}] = name
		}
	}
	if ev.Type == llm.EventToolInputDelta && r.callNames != nil {
		if name, ok := r.callNames[callKey{0, ev.Index}]; ok {
			if schema := r.schemas[name]; schema != nil {
				ev.Delta = string(integralArguments([]byte(ev.Delta), schema))
			}
		}
	}
	if ev.Type == llm.EventCandidate && ev.Candidate != nil {
		c := *ev.Candidate
		key := callKey{c.Index, c.Item}
		switch c.Kind {
		case llm.CandidateToolCallStart:
			if client, ok := r.names[c.Name]; ok {
				c.Name = client
			}
			if r.callNames != nil {
				r.callNames[key] = c.Name
			}
		case llm.CandidateToolInputDelta:
			if name, ok := r.callNames[key]; ok {
				if schema := r.schemas[name]; schema != nil {
					c.Delta = string(integralArguments([]byte(c.Delta), schema))
				}
			}
		}
		ev.Candidate = &c
	}
	if err := r.calls.Check(ev); err != nil {
		return err
	}
	return r.out.Send(r.redact.Event(ev))
}

func replyNames(req llm.Request, tr *llm.Translation) map[string]string {
	if tr.Passthrough {
		return nil
	}
	back := map[string]string{}
	for from, to := range geminigenerate.ToolNames(req) {
		back[to] = from
	}
	return back
}

func relayStream(ctx context.Context, body io.Reader, out sender, bodyReaches bool) error {
	r := sse.NewReader(body)
	dec := geminigenerate.NewStreamDecoder()
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

func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	ctx = withAccountID(ctx, call.Account.ID)
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	cred := call.Account.Credential
	if s.Vertex != nil || (cred != nil && cred.Kind != protocol.CredentialAPIKey) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only a Gemini API key can be checked without a generation", Scope: llm.ScopeCredential}
	}
	if cred == nil || cred.APIKey == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the account has no API key", Scope: llm.ScopeCredential}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/v1beta/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("X-Goog-Api-Key", cred.APIKey)
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
		return nil, upstreamErrorFor(ctx, resp, "", nil)
	}
	var list struct {
		Models []json.RawMessage `json:"models"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if json.Unmarshal(raw, &list) != nil || list.Models == nil {
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	}
	return &protocol.CheckResult{Detail: fmt.Sprintf("the upstream lists %d models", len(list.Models))}, nil
}

func upstreamError(resp *http.Response, model string) *llm.Error {
	var ctx context.Context
	if resp != nil && resp.Request != nil {
		ctx = resp.Request.Context()
	}
	return upstreamErrorFor(ctx, resp, model, nil)
}

func upstreamErrorFor(ctx context.Context, resp *http.Response, model string, redact *protocol.Redactor) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	parsed, hasParsed := parseGoogleError(raw)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message, ProviderCode: parsed.Error.Status}
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
	case s == http.StatusForbidden && parsed.hasReason("CONSUMER_SUSPENDED"):
		e.Code, e.Scope, e.Retryable = llm.CodeAccountClosed, llm.ScopeCredential, true
	case isAccountClosed(s, e.Message):
		e.Code, e.Scope, e.Retryable = llm.CodeAccountClosed, llm.ScopeCredential, true
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusBadRequest && (parsed.reason() == "API_KEY_INVALID" || strings.Contains(e.Message, "API key not valid")):
		// Google answers a key it does not know with a 400, not a 401.
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		// Gemini names the model when it does not have it, or does not have it for the method
		// (an embedding call on a chat model): then it did not run the request, and another account
		// may serve the model. The embedding calls turn this into a refusal on the account instead
		// (embeddingNotFound), so their 404 does not block the model's generation.
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		now := time.Now()
		observeRateLimit(ctx, resp.Header, raw, &parsed, hasParsed, model, redact, now)
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			e.Scope = llm.ScopeCredential
		}
		if parsed.Error.Status == "RESOURCE_EXHAUSTED" && parsed.reason() == "QUOTA_EXHAUSTED" {
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), now)
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = parsed.retryDelay()
		}
	case s == http.StatusServiceUnavailable:
		// Not retry-safe: Google answers 503 for an overloaded model, which may have started.
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
		if s == 529 {
			e.RetryAfterMS = sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		// Google's error table gives this as a cause of 500 INTERNAL: the request's, not the account's.
		if s == http.StatusInternalServerError && strings.Contains(strings.ToLower(e.Message), "input context is too long") {
			e.Status = http.StatusBadRequest
			_ = llm.MarkContextOverflow(e)
		}
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
		_ = llm.MarkContextOverflow(e)
	}
	return e
}

func isAccountClosed(status int, msg string) bool {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return false
	}
	m := strings.ToLower(msg)
	for _, s := range []string{
		"account_deactivated",
		"account has been deactivated",
		"this account is deactivated",
		"account has been disabled",
		"your account has been suspended",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
