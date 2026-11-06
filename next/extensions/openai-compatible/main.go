// Command openai-compatible is a pluxy provider extension for services that speak OpenAI Chat
// Completions or OpenAI Responses with an API key: OpenAI itself and the many services and local
// servers that copy its API.
//
// Account settings:
//
//	{"base_url": "https://api.openai.com/v1", "wire_api": "chat"}
//
// wire_api is the API the upstream speaks: "chat" (the default, POST {base_url}/chat/completions)
// or "responses" (POST {base_url}/responses). The name follows the Codex CLI's model provider
// setting. A request in the upstream's own format is sent unchanged; a Responses request to a
// Chat upstream is translated, and what the translation could not carry is reported with the
// reply. The account's credential must be of kind api_key; it is sent as a bearer token.
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
)

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context.
var client = &http.Client{Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout: 30 * time.Second,
	ForceAttemptHTTP2:   true,
	MaxIdleConns:        100,
	IdleConnTimeout:     90 * time.Second,
}}

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
	switch s.WireAPI {
	case "":
		s.WireAPI = wireChat
	case wireChat, wireResponses:
	default:
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("account setting wire_api must be %q or %q", wireChat, wireResponses), Scope: llm.ScopeCredential}
	}
	cred := call.Account.Credential
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return &llm.Error{Code: llm.CodeAuth, Message: "account has no API key", Scope: llm.ScopeCredential}
	}

	body, tr, err := buildBody(call.Request, s.WireAPI)
	if err != nil {
		return err
	}
	// The report travels on the reply's first event.
	send := &reporting{Stream: out, tr: tr}
	endpoint := "/chat/completions"
	if s.WireAPI == wireResponses {
		endpoint = "/responses"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
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
		return upstreamError(resp)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayStream(ctx, resp.Body, send, s.WireAPI)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	decode := openaichat.DecodeResponse
	if s.WireAPI == wireResponses {
		decode = openairesponses.DecodeResponse
	}
	events, err := decode(raw)
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

// buildBody returns the upstream request body for req and how it was built. A request in the
// upstream's own format is sent unchanged. A Responses request to a Chat upstream is translated;
// under the reject drop policy a translation that drops anything is refused.
func buildBody(req llm.Request, wire string) ([]byte, *llm.Translation, error) {
	target := llm.FormatOpenAIChat
	if wire == wireResponses {
		target = llm.FormatOpenAIResponses
	}
	tr := &llm.Translation{To: target}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == target {
			tr.Passthrough = true
			return n.Body, tr, nil
		}
	}
	if target == llm.FormatOpenAIResponses {
		// shortcut: no request is rebuilt as Responses yet, so only Responses clients can use a
		// Responses upstream; add an encoder when another client format needs one.
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: "this account's upstream speaks OpenAI Responses; requests in " + tr.From + " are not translated to it", Scope: llm.ScopeRequest}
	}
	body, losses, err := openaichat.EncodeRequest(req)
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
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeRequest,
			Message: "Chat Completions cannot carry parts of this request: " + strings.Join(paths, ", ")}
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

// relayStream decodes the upstream stream and sends each event as soon as it is decoded. A
// blocked send blocks the next upstream read.
func relayStream(ctx context.Context, body io.Reader, out eventSender, wire string) error {
	r := sse.NewReader(body)
	var dec streamDecoder = openaichat.NewStreamDecoder()
	if wire == wireResponses {
		dec = openairesponses.NewStreamDecoder()
	}
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

// upstreamError turns a non-2xx upstream response into a structured error.
func upstreamError(resp *http.Response) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message}
	if c := parsed.Error.Code; c != nil {
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
	case s == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case s == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case s == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
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
