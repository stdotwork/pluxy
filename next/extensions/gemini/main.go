// Command gemini is pluxy's provider extension for Google's Gemini models: the Gemini API with an
// API key, and Vertex AI with a service account or an API key (vertex.go).
//
// Account settings (all optional):
//
//	{"base_url": "https://generativelanguage.googleapis.com"}
//
// base_url is where the Gemini API lives; requests go to
// {base_url}/v1beta/models/{model}:generateContent, or :streamGenerateContent?alt=sse for a
// stream, with the API key in x-goog-api-key. Vertex accounts are described in vertex.go.
//
// An account check (core's check call) of a Gemini API account reads the model list,
// GET {base_url}/v1beta/models with x-goog-api-key, which costs the account nothing. A Vertex
// account, and any credential that is not an API key, is left not checked, without a request.
//
// A Gemini GenerateContent request is sent unchanged, thought signatures included. Requests in
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
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
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
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		CountTokens:  countTokens,
		Models:       listModels,
		Check:        check,
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
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	},
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

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr}
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
	resp, err := postBody(ctx, t, body, call.Request.Stream, call.Request.Model)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, err := geminigenerate.DecodeResponse(raw)
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
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		// A failed dial never reached the upstream, so trying again is safe.
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		e := upstreamError(resp, model)
		if t.sa != nil && resp.StatusCode == http.StatusUnauthorized {
			tokens.invalidate(t.sa, t.token)
		}
		return nil, e
	}
	return resp, nil
}

// buildBody returns the GenerateContent body for req and how it was built. A GenerateContent
// request is sent unchanged; any other is rebuilt from the model, with its reasoning effort as
// thinking settings. A thinking suffix on the model name sets the thinking settings either way,
// in place of the request's own. Under the reject drop policy a translation that drops anything
// is refused.
func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: llm.FormatGeminiGenerate}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == llm.FormatGeminiGenerate {
			tr.Passthrough = true
			body := n.Body
			if s := req.Options.ThinkingSuffix; s != "" {
				var err error
				if body, _, err = geminigenerate.ApplyEffort(body, req.Model, s); err != nil {
					return nil, nil, err
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
	body, losses, err := geminigenerate.EncodeRequest(req)
	if err != nil {
		return nil, nil, err
	}
	losses = append(extra, losses...)
	effort := req.Options.ReasoningEffort
	if s := req.Options.ThinkingSuffix; s != "" {
		effort = s
	}
	if effort != "" {
		var applied bool
		body, applied, err = geminigenerate.ApplyEffort(body, req.Model, effort)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			losses = dropLoss(losses, "reasoning_effort")
		}
	}
	tr.Losses = losses
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		var paths []string
		for _, l := range losses {
			if l.Kind == llm.LossDropped {
				paths = append(paths, l.Path)
			}
		}
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
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

// reporting adds the translation report to the reply's response.start event.
type reporting struct {
	*sdk.Stream
	tr     *llm.Translation
	redact *protocol.Redactor // nil, or what else to remove from upstream data
}

func (r *reporting) Send(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		info := *ev.Response
		info.Translation = r.tr
		ev.Response = &info
	}
	return r.Stream.Send(r.redact.Event(ev))
}

// relayStream decodes the upstream GenerateContent stream and sends each event as soon as it is
// decoded. A blocked send blocks the next upstream read.
func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }) error {
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

// googleError is the error body Google's APIs answer with.
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string `json:"@type"`
			Reason     string `json:"reason"`
			RetryDelay string `json:"retryDelay"`
		} `json:"details"`
	} `json:"error"`
}

// reason returns the ErrorInfo reason in the error's details, such as API_KEY_INVALID.
func (g *googleError) reason() string {
	for _, d := range g.Error.Details {
		if d.Reason != "" {
			return d.Reason
		}
	}
	return ""
}

// retryDelay returns the RetryInfo delay in the error's details, in milliseconds, or 0.
func (g *googleError) retryDelay() int64 {
	for _, d := range g.Error.Details {
		if d.RetryDelay == "" {
			continue
		}
		if dur, err := time.ParseDuration(d.RetryDelay); err == nil && dur > 0 {
			return dur.Milliseconds()
		}
	}
	return 0
}

// check asks the Gemini API for its model list with an API key, which costs nothing and generates
// nothing. A Vertex account, or a credential of another kind, is answered unsupported without a
// request.
func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
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
		return nil, upstreamError(resp, "")
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed googleError
	_ = json.Unmarshal(raw, &parsed)
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
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeCredential, true
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
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
