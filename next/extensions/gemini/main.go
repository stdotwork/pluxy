// Command gemini is pluxy's provider extension for Google's Gemini models: the Gemini API with an
// API key, and Vertex AI with a service account or an API key (vertex.go).
//
// Account settings (all optional):
//
//	{"base_url": "https://generativelanguage.googleapis.com", "interactions": false}
//
// base_url is where the Gemini API lives; requests go to
// {base_url}/v1beta/models/{model}:generateContent, or :streamGenerateContent?alt=sse for a
// stream, with the API key in x-goog-api-key. Vertex accounts are described in vertex.go.
//
// An account check (core's check call) of a Gemini API account reads the model list,
// GET {base_url}/v1beta/models with x-goog-api-key, which costs the account nothing. A Vertex
// account, and any credential that is not an API key, is left not checked, without a request.
//
// An account whose settings say "interactions": true also serves Gemini's Interactions API to
// Interactions clients, as endpoint calls (interactions.go); any other account answers them
// unsupported.
//
// A Gemini GenerateContent request is sent as the client wrote it, thought signatures included,
// apart from the repairs Google needs (tool schemas in schema.go; turns and the order of function
// responses in turns.go). A reply that ends with MALFORMED_FUNCTION_CALL before the client has been
// sent any content (a thought, answer text or a call) is asked for once more (relayReply); a
// stream is never held back past its first content, so reasoning streams live. Requests in
// other formats (Chat Completions, Responses, Messages) are translated to GenerateContent, with
// the client's reasoning effort turned into Gemini's thinking settings (thinking.go).
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
	"strconv"
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

// defaultBaseURL is the Gemini API.
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

// settings are an account's settings.
type settings struct {
	BaseURL string `json:"base_url"`
	// Vertex, when set, sends the account's requests to Vertex AI (vertex.go).
	Vertex *vertexSettings `json:"vertex"`
	// Interactions serves Gemini's Interactions API to Interactions clients (interactions.go).
	Interactions bool `json:"interactions"`
}

// readSettings reads an account's settings and fills in the defaults.
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

// sameOrigin refuses a redirect to another scheme or host; the redirect reply is then treated as
// the upstream's answer.
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

// method returns the GenerateContent method for a request, with the query a stream needs.
func method(stream bool) string {
	if stream {
		return ":streamGenerateContent?alt=sse"
	}
	return ":generateContent"
}

// target is where one request goes and how it is authorised.
type target struct {
	url       string
	authorize func(http.Header)
	// token and sa are set for a service account: the access token fetched for this request, which
	// core does not know to redact, and the key it came from.
	token string
	sa    *serviceAccount
}

// resolve returns where an account's GenerateContent request for model goes.
func resolve(ctx context.Context, s settings, cred *protocol.Credential, model string, stream bool) (target, error) {
	return resolveMethod(ctx, s, cred, model, method(stream))
}

// resolveMethod returns where an account's request for model's method m (such as
// ":countTokens", with any query) goes.
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

// sender takes the events of a reply: the call's stream, or a test's recorder.
type sender interface{ Send(llm.Event) error }

// generateTo answers a generate call, sending the reply's events to out.
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
		send.callNames = map[int]string{}
	}
	t, err := resolve(ctx, s, call.Account.Credential, call.Request.Model, call.Request.Stream)
	if err != nil {
		return err
	}
	if t.token != "" {
		// Core redacts what the credential holds; the fetched access token is redacted here, in
		// case an upstream or a proxy between echoes it.
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
	// A reply that ends with MALFORMED_FUNCTION_CALL (the model wrote a function call Gemini
	// could not parse) is asked for once more, as long as the client has seen none of it.
	for first := true; ; first = false {
		resp, err := postBody(ctx, t, body, call.Request.Stream, call.Request.Model)
		if err != nil {
			return err
		}
		again, err := relayReply(ctx, resp, send, first)
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

// relayReply sends one upstream reply to send and closes its body. With mayRetry, a reply that
// ends with MALFORMED_FUNCTION_CALL before send has been given any content is not sent: again
// reports that it should be asked for once more. A stream is held back from send only until its
// first content event (holding), so a reply that fails before that can still be asked for again,
// while one that fails after content went out reaches the client as the error.
func relayReply(ctx context.Context, resp *http.Response, send sender, mayRetry bool) (again bool, err error) {
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if !mayRetry {
			return false, relayStream(ctx, resp.Body, send)
		}
		h := &holding{out: send}
		err := relayStream(ctx, resp.Body, h)
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
	events, err := geminigenerate.DecodeResponse(raw)
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

// holding keeps back from out the events that carry no content (the response start and native
// chunks that decode to nothing) until the first content event of any kind (a thought, answer
// text or a call) or the end of the reply; then it sends what it held and every event after it.
// Until then the reply can be dropped and asked for again without the client seeing any of it.
type holding struct {
	out  sender
	held []llm.Event
	open bool // the held events have been sent
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

// flush sends the held events and lets every later one through.
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

// postBody sends body to target t and returns the upstream's successful reply; a failure is
// returned as the error to report. stream asks for a stream.
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

// buildBody returns the GenerateContent body for req and how it was built. A GenerateContent
// request is sent unchanged; any other is rebuilt from the model, with its reasoning effort as
// thinking settings and any Messages thinking.display or Responses reasoning.summary as
// includeThoughts. A thinking suffix on the model name sets the thinking settings either way, in
// place of the request's own. Under the reject drop policy a translation that drops anything is
// refused.
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

// buildBodyOn builds the body as buildBody does for the Gemini API, for the account with settings
// s. A translated request's service tier is left out of the body, and reported as dropped, for an
// account that does not take one (takesServiceTier).
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
		// Messages makes max_tokens mandatory, so a Messages client's value says nothing about
		// the limit it wants, and it is often above what a Gemini model can write (Claude
		// Code sends 32000 and more). Today's core leaves it out too; Gemini then uses the
		// model's own limit.
		req.Options.MaxOutputTokens = nil
		extra = append(extra, llm.Loss{Kind: llm.LossDropped, Path: "max_tokens", Detail: "a Messages client's required max_tokens is left to the Gemini model's own limit"})
	}
	body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{ServiceTier: takesServiceTier(s)})
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

// dropLoss removes the loss at path from losses.
func dropLoss(losses []llm.Loss, path string) []llm.Loss {
	out := losses[:0]
	for _, l := range losses {
		if l.Path != path {
			out = append(out, l)
		}
	}
	return out
}

// reporting adds the translation report to the reply's response.start event, and gives each tool
// call the client's own name for the tool when the request was sent under another (names).
type reporting struct {
	out    sender
	tr     *llm.Translation
	redact *protocol.Redactor // nil, or what else to remove from upstream data
	known  []string           // the account's own Google project and identity, which an error must not name
	names  map[string]string  // GenerateContent name -> the client's
	// schemas are the request's function schemas by the client's tool name, and callNames the
	// client's name of each open call, for integralArguments; both nil on a passthrough body.
	schemas   map[string]json.RawMessage
	callNames map[int]string
	calls     *openairesponses.CustomCallCheck
}

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
			r.callNames[ev.Index] = name
		}
	}
	if ev.Type == llm.EventToolInputDelta && r.callNames != nil {
		if name, ok := r.callNames[ev.Index]; ok {
			if schema := r.schemas[name]; schema != nil {
				ev.Delta = string(integralArguments([]byte(ev.Delta), schema))
			}
		}
	}
	if err := r.calls.Check(ev); err != nil {
		return err
	}
	return r.out.Send(r.redact.Event(ev))
}

// replyNames maps each tool name a translated request was sent under back to the client's
// (geminigenerate.ToolNames). A GenerateContent body passed through keeps the client's names.
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

// relayStream decodes the upstream GenerateContent stream and sends each event as soon as it is
// decoded. A blocked send blocks the next upstream read.
func relayStream(ctx context.Context, body io.Reader, out sender) error {
	r := sse.NewReader(body)
	dec := geminigenerate.NewStreamDecoder()
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

// parseGoogleError / reason / retryDelay are defined in quota.go.

// check asks the Gemini API for its model list with an API key, which costs nothing and generates
// nothing. A Vertex account, or a credential of another kind, is answered unsupported without a
// request.
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

// upstreamError turns a non-2xx GenerateContent response into a structured error. model is the
// model the request asked for.
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
		// A redirect sameOrigin refused to follow.
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
		// Gemini names the model when it does not have it: then it did not run the request, and
		// another account may serve the model.
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		now := time.Now()
		observeRateLimit(ctx, resp.Header, raw, &parsed, hasParsed, model, redact, now)
		// Only the model waits on this account, as today's core cools only the model that answered.
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			// A call for no model (login or the model list): the account is the one limited.
			e.Scope = llm.ScopeCredential
		}
		if parsed.Error.Status == "RESOURCE_EXHAUSTED" && parsed.reason() == "QUOTA_EXHAUSTED" {
			// A quota is used up, not a request rate exceeded (RATE_LIMIT_EXCEEDED).
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), now)
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = parsed.retryDelay()
		}
	case s == http.StatusServiceUnavailable:
		// Not retry-safe: Google answers 503 for an overloaded model, which may have started.
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		// The upstream may have run the request before failing, so retrying is not known safe.
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
		if s == 529 {
			e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
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

// isAccountClosed reports whether a 401 or 403 reply says the account itself has been
// deactivated, disabled or suspended (distinct from VALIDATION_REQUIRED verification prompts).
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
