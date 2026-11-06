// Command openai-compatible is a pluxy provider extension for services that speak OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages or Gemini GenerateContent with an API key:
// OpenAI itself and the many services and local servers that copy its API, some of which also
// serve a Messages or GenerateContent endpoint.
//
// Account settings:
//
//	{"base_url": "https://api.openai.com/v1", "wire_api": "chat"}
//
// wire_api is the API the upstream speaks: "chat" (the default, POST {base_url}/chat/completions),
// "responses" (POST {base_url}/responses), "messages" (POST {base_url}/messages) or "gemini"
// (POST {base_url}/models/{model}:generateContent, or :streamGenerateContent?alt=sse; base_url
// ends in /v1beta). The name follows the Codex CLI's model provider setting. A request in the
// upstream's own format is sent unchanged; a request in another format is translated where a
// translation exists, and what the translation could not carry is reported with the reply. The
// account's credential must be of kind api_key; it is sent as a bearer token, for a Messages
// upstream in x-api-key with the client's anthropic-version (2023-06-01 when the client sent
// none) and anthropic-beta headers, and for a Gemini upstream in x-goog-api-key.
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

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/openai-compatible"
	version = "0.1.0"
)

func main() {
	if err := sdk.Serve(sdk.Extension{ID: id, Version: version, Generate: generate}); err != nil {
		fmt.Fprintln(os.Stderr, "openai-compatible:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL string `json:"base_url"`
	WireAPI string `json:"wire_api"`
}

// Wire APIs.
const (
	wireChat      = "chat"
	wireResponses = "responses"
	wireMessages  = "messages"
	wireGemini    = "gemini"
)

// anthropicVersion is the Messages API version sent when the client named none.
const anthropicVersion = "2023-06-01"

// wires maps a wire API to its upstream format and endpoint.
var wires = map[string]struct{ format, endpoint string }{
	wireChat:      {llm.FormatOpenAIChat, "/chat/completions"},
	wireResponses: {llm.FormatOpenAIResponses, "/responses"},
	wireMessages:  {llm.FormatAnthropicMessages, "/messages"},
	// The model and the method are added per request (see endpoint).
	wireGemini: {llm.FormatGeminiGenerate, "/models/"},
}

// endpoint returns the path after base_url for a request to wire. GenerateContent names the
// model and whether to stream in the path.
func endpoint(wire string, req llm.Request) string {
	e := wires[wire].endpoint
	if wire != wireGemini {
		return e
	}
	if req.Stream {
		return e + url.PathEscape(req.Model) + ":streamGenerateContent?alt=sse"
	}
	return e + url.PathEscape(req.Model) + ":generateContent"
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host: Go drops Authorization on a redirect to another host but not x-api-key or
// x-goog-api-key, so following one would send the account's key elsewhere.
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

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	var s settings
	if len(call.Account.Settings) > 0 {
		if err := json.Unmarshal(call.Account.Settings, &s); err != nil {
			return &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	if s.BaseURL == "" {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings need base_url", Scope: llm.ScopeCredential}
	}
	if s.WireAPI == "" {
		s.WireAPI = wireChat
	}
	wire, ok := wires[s.WireAPI]
	if !ok {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("account setting wire_api must be %q, %q, %q or %q", wireChat, wireResponses, wireMessages, wireGemini), Scope: llm.ScopeCredential}
	}
	cred := call.Account.Credential
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return &llm.Error{Code: llm.CodeAuth, Message: "account has no API key", Scope: llm.ScopeCredential}
	}

	body, tr, err := buildBody(call.Request, wire.format)
	if err != nil {
		return err
	}
	// The report travels on the reply's first event.
	send := &reporting{Stream: out, tr: tr}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+endpoint(s.WireAPI, call.Request), bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	switch wire.format {
	case llm.FormatGeminiGenerate:
		req.Header.Set("X-Goog-Api-Key", cred.APIKey)
	case llm.FormatAnthropicMessages:
		req.Header.Set("X-Api-Key", cred.APIKey)
		req.Header.Set("Anthropic-Version", anthropicVersion)
		if n := call.Request.Native; n != nil && n.Format == wire.format {
			for _, h := range anthropicmessages.Headers {
				if v := n.Headers[h]; v != "" {
					req.Header.Set(h, v)
				}
			}
		}
	default:
		req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	}
	if call.Request.Stream {
		req.Header.Set("Accept", "text/event-stream")
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
		return relayStream(ctx, resp.Body, send, wire.format)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	events, err := decoders[wire.format].response(raw)
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

// formatNames name the upstream formats in messages to clients.
var formatNames = map[string]string{
	llm.FormatOpenAIChat:        "Chat Completions",
	llm.FormatOpenAIResponses:   "OpenAI Responses",
	llm.FormatAnthropicMessages: "Anthropic Messages",
	llm.FormatGeminiGenerate:    "Gemini GenerateContent",
}

// buildBody returns the upstream request body for req in format target and how it was built. A
// request in the upstream's own format is sent unchanged. Requests are translated to Chat from any
// client format, to Responses from Messages and Gemini, and to Messages from Gemini; under the
// reject drop policy a translation that drops anything is refused.
func buildBody(req llm.Request, target string) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: target}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == target {
			tr.Passthrough = true
			return n.Body, tr, nil
		}
	}
	var encode func(llm.Request) ([]byte, []llm.Loss, error)
	switch {
	case target == llm.FormatOpenAIChat:
		encode = openaichat.EncodeRequest
	case target == llm.FormatOpenAIResponses && (tr.From == llm.FormatAnthropicMessages || tr.From == llm.FormatGeminiGenerate):
		// shortcut: only Messages and Gemini requests are rebuilt as Responses; Chat requests
		// have no corpus cases for it yet. Allow them once they do.
		encode = openairesponses.EncodeRequest
	case target == llm.FormatAnthropicMessages && tr.From == llm.FormatGeminiGenerate:
		// shortcut: only Gemini requests are rebuilt as Messages here; the Claude extension
		// rebuilds Chat and Responses requests too. Allow them here once this extension's
		// Messages accounts have corpus cases for them.
		encode = anthropicmessages.EncodeRequest
	default:
		// shortcut: nothing else is rebuilt as Messages here (the Claude extension does it for
		// Claude), and nothing is rebuilt as GenerateContent (the Gemini provider brings that).
		// The refusal is about this account's upstream, not the request: an account that speaks
		// the client's format can still serve it, so core may try one (scope credential,
		// retryable: nothing reached the upstream).
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's upstream speaks " + formatNames[target] + "; requests in " + tr.From + " are not translated to it"}
	}
	body, losses, err := encode(req)
	if err != nil {
		return nil, nil, err
	}
	tr.Losses = losses
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		var paths []string
		for _, l := range losses {
			if l.Kind == llm.LossDropped {
				paths = append(paths, l.Path)
			}
		}
		// Another account may speak the client's format and carry it whole.
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: formatNames[target] + " cannot carry parts of this request: " + strings.Join(paths, ", ")}
	}
	return body, tr, nil
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

// eventSender is what the reply is sent to.
type eventSender interface{ Send(llm.Event) error }

// streamDecoder decodes one upstream stream.
type streamDecoder interface {
	Decode(data []byte) ([]llm.Event, error)
	Close() ([]llm.Event, error)
	// Usage is the latest usage the upstream reported, put on an error when the stream breaks.
	Usage() *llm.Usage
}

// decoders read each upstream format's streamed and non-streamed replies.
var decoders = map[string]struct {
	stream   func() streamDecoder
	response func([]byte) ([]llm.Event, error)
}{
	llm.FormatOpenAIChat:        {func() streamDecoder { return openaichat.NewStreamDecoder() }, openaichat.DecodeResponse},
	llm.FormatOpenAIResponses:   {func() streamDecoder { return openairesponses.NewStreamDecoder() }, openairesponses.DecodeResponse},
	llm.FormatAnthropicMessages: {func() streamDecoder { return anthropicmessages.NewStreamDecoder() }, anthropicmessages.DecodeResponse},
	llm.FormatGeminiGenerate:    {func() streamDecoder { return geminigenerate.NewStreamDecoder() }, geminigenerate.DecodeResponse},
}

// relayStream decodes the upstream stream and sends each event as soon as it is decoded. A
// blocked send blocks the next upstream read.
func relayStream(ctx context.Context, body io.Reader, out eventSender, upstreamFormat string) error {
	r := sse.NewReader(body)
	dec := decoders[upstreamFormat].stream()
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

// upstreamError turns a non-2xx upstream response into a structured error. model is the model
// the request asked for.
func upstreamError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Status  string `json:"status"` // Google's error status, such as RESOURCE_EXHAUSTED
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message}
	if c := parsed.Error.Code; parsed.Error.Status != "" {
		e.ProviderCode = parsed.Error.Status
	} else if c != nil {
		e.ProviderCode = fmt.Sprint(c)
	} else {
		e.ProviderCode = parsed.Error.Type
	}
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
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		if modelRefused(e, model) {
			// The upstream confirmed it does not serve the model, so it did not run the
			// request: another account may serve the model.
			e.Retryable = true
		}
	case s == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeCredential, true
		if e.ProviderCode == "insufficient_quota" {
			e.Code = llm.CodeQuota
		}
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"))
	case s == http.StatusServiceUnavailable:
		e.Code, e.Scope, e.Retryable = llm.CodeUnavailable, llm.ScopeEndpoint, true
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"))
	case s >= 500:
		// The upstream may have run the request before failing, so retrying is not known safe.
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// modelRefused reports whether a 404 is the upstream saying it does not have the requested
// model, as OpenAI says with code model_not_found and Anthropic and Gemini say by naming the
// model in the message, rather than a path or other resource it does not serve.
func modelRefused(e *llm.Error, model string) bool {
	return e.ProviderCode == "model_not_found" || (model != "" && strings.Contains(e.Message, model))
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
