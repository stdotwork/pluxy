// Command antigravity is pluxy's provider extension for Google Antigravity: Gemini and Claude
// models served through a Google account signed in to Antigravity (login.go), on Google's Cloud
// Code API.
//
// Account settings (all optional):
//
//	{"base_url": "https://daily-cloudcode-pa.googleapis.com", "user_agent": "..."}
//
// base_url is the Cloud Code host; requests go to {base_url}/v1internal:generateContent, or
// :streamGenerateContent?alt=sse for a stream, as Antigravity sends them. user_agent replaces the
// Antigravity app's User-Agent. Tests move Google's login to a fake with auth_base_url
// (sdk.TestLoginHost); in a normal run it is ignored. callback_listen false keeps the login from listening for the browser's return.
//
// A request is built as a Gemini GenerateContent body (a Gemini client's own body, or a
// translation of any other format) and sent inside Cloud Code's envelope with the account's
// Google Cloud project (body.go). The reply's GenerateContent response is taken out of the
// envelope's response field.
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
	"regexp"
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
	id      = "pluxy/antigravity"
	version = "0.1.0"
)

// defaultBaseURL is the Cloud Code host Antigravity uses (today's core sends every request there,
// with no fallback to the other hosts).
const defaultBaseURL = "https://daily-cloudcode-pa.googleapis.com"

// appVersion is the Antigravity app version the requests name, with its changelist, as today's
// core sends them when it has not looked up a newer version. shortcut: fixed, where today's core
// also polls the app's update feed every few hours; update it with the app.
const (
	appVersion    = "2.29.0"
	appChangelist = "1013060670"
)

// userAgent is the Antigravity app's User-Agent on Cloud Code.
var userAgent = "antigravity/hub/" + appVersion + " (aidev_client; os_type=darwin; arch=arm64; cl=" + appChangelist + ")"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		Models:       listModels,
		Quota:        quota,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "antigravity:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL        string `json:"base_url"`
	UserAgent      string `json:"user_agent"`
	AuthBaseURL    string `json:"auth_base_url"`
	CallbackListen *bool  `json:"callback_listen"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL
	}
	s.AuthBaseURL = strings.TrimRight(sdk.TestLoginHost(s.AuthBaseURL), "/")
	if s.UserAgent == "" {
		s.UserAgent = userAgent
	}
	return s, nil
}

// credentialData is what an account's credential keeps besides its tokens.
type credentialData struct {
	// ProjectID is the Google Cloud project Cloud Code bills the account's requests to, found
	// or made at login.
	ProjectID string `json:"project_id"`
}

func readData(c *protocol.Credential) credentialData {
	var d credentialData
	if c != nil && len(c.Data) > 0 {
		_ = json.Unmarshal(c.Data, &d)
	}
	return d
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, since Go keeps the bearer token on a redirect to another host.
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

// streamOnly reports whether Cloud Code answers model only on its stream method, as today's core
// treats Claude models, Gemini 3 Pro and the image model: a request that wants one reply is
// streamed and gathered.
func streamOnly(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "claude") || strings.Contains(m, "gemini-3-pro") || strings.Contains(m, "gemini-3.1-flash-image")
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	c := call.Account.Credential
	if c == nil || c.Kind != protocol.CredentialOAuth || c.AccessToken == "" {
		return &llm.Error{Code: llm.CodeAuth, Message: "the account has no Antigravity login", Scope: llm.ScopeCredential}
	}
	data := readData(c)
	if data.ProjectID == "" {
		return &llm.Error{Code: llm.CodeAuth, Message: "the account's login names no Google Cloud project; log in again", Scope: llm.ScopeCredential}
	}
	inner, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	body, err := envelope(inner, call.Request.Model, data.ProjectID)
	if err != nil {
		return err
	}
	stream := call.Request.Stream || streamOnly(call.Request.Model)
	endpoint := s.BaseURL + "/v1internal:generateContent"
	if stream {
		endpoint = s.BaseURL + "/v1internal:streamGenerateContent?alt=sse"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("User-Agent", s.UserAgent)

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
	send := &reporting{Stream: out, tr: tr}
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
	events, err := geminigenerate.DecodeResponse(unwrap(raw))
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

// unwrap returns the GenerateContent response inside a Cloud Code reply ({"response": {...},
// "traceId": ...}), or the reply itself when it has no envelope.
func unwrap(raw []byte) []byte {
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Response) > 0 && env.Response[0] == '{' {
		return env.Response
	}
	return raw
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

// relayStream decodes the upstream stream and sends each event as soon as it is decoded. A
// blocked send blocks the next upstream read. An error event after the first sent event is not
// retry-safe, as the model already ran.
func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }) error {
	r := sse.NewReader(body)
	dec := geminigenerate.NewStreamDecoder()
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
		case strings.TrimSpace(e.Data) == "[DONE]":
			continue
		default:
			if ge := streamError([]byte(e.Data)); ge != nil {
				ge.Usage = dec.Usage()
				ge.Retryable = ge.Retryable && !sent
				return ge
			}
			events, err = dec.Decode(unwrap([]byte(e.Data)))
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

// streamError returns the error an event of the stream carries ({"error": {...}}), or nil.
func streamError(data []byte) *llm.Error {
	var g googleError
	if json.Unmarshal(data, &g) != nil || g.Error.Message == "" && g.Error.Code == 0 {
		return nil
	}
	status := g.Error.Code
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	e := classify(status, &g, data, "", nil)
	return e
}

// googleError is the error body Google's APIs answer with.
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string            `json:"@type"`
			Reason     string            `json:"reason"`
			RetryDelay string            `json:"retryDelay"`
			Metadata   map[string]string `json:"metadata"`
		} `json:"details"`
	} `json:"error"`
}

// reason returns the ErrorInfo reason in the error's details, such as QUOTA_EXHAUSTED.
func (g *googleError) reason() string {
	for _, d := range g.Error.Details {
		if d.Reason != "" {
			return d.Reason
		}
	}
	return ""
}

var afterSeconds = regexp.MustCompile(`after\s+((?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s))`)

// retryDelay returns how long the error asks to wait, in milliseconds, or 0: RetryInfo's delay,
// then ErrorInfo's quotaResetDelay, then a delay the message names ("... after 30s."), as
// today's core reads them.
func (g *googleError) retryDelay() int64 {
	for _, d := range g.Error.Details {
		if dur, err := time.ParseDuration(d.RetryDelay); d.RetryDelay != "" && err == nil && dur > 0 {
			return dur.Milliseconds()
		}
	}
	for _, d := range g.Error.Details {
		if v := d.Metadata["quotaResetDelay"]; v != "" {
			if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
				return dur.Milliseconds()
			}
		}
	}
	if m := afterSeconds.FindStringSubmatch(strings.ToLower(g.Error.Message)); m != nil {
		if dur, err := time.ParseDuration(m[1]); err == nil && dur > 0 {
			return dur.Milliseconds()
		}
	}
	return 0
}

// longWait is the delay from which a rate limit is taken as a used-up quota, as today's core
// decides.
const longWait = 5 * time.Minute

// upstreamError turns a non-2xx Cloud Code reply into a structured error. model is the model the
// request asked for.
func upstreamError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed googleError
	_ = json.Unmarshal(raw, &parsed)
	return classify(resp.StatusCode, &parsed, raw, model, resp.Header)
}

func classify(status int, parsed *googleError, raw []byte, model string, h http.Header) *llm.Error {
	e := &llm.Error{Status: status, Message: parsed.Error.Message, ProviderCode: parsed.Error.Status}
	if e.Message == "" {
		e.Message = fmt.Sprintf("upstream returned %d %s", status, http.StatusText(status))
	}
	if json.Valid(raw) {
		e.Raw = raw
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
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case status == http.StatusTooManyRequests:
		// Cloud Code limits each model on its own, so a 429 holds back only the model, as today's
		// core cools only the model that answered; a used-up credit balance is the account's.
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if parsed.reason() == "INSUFFICIENT_G1_CREDITS_BALANCE" {
			e.Scope = llm.ScopeCredential
		}
		if h != nil {
			e.RetryAfterMS = retryAfter(h.Get("Retry-After"), time.Now())
		}
		if e.RetryAfterMS == 0 {
			e.RetryAfterMS = parsed.retryDelay()
		}
		if quotaUsedUp(parsed, raw, e.RetryAfterMS) {
			e.Code = llm.CodeQuota
		}
	case status == http.StatusServiceUnavailable:
		// Not retry-safe: Google answers 503 for an overloaded model, which may have started.
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		if h != nil {
			e.RetryAfterMS = retryAfter(h.Get("Retry-After"), time.Now())
		}
	case status >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// quotaUsedUp reports whether a 429 says the account's quota is used up rather than that it is
// sending too fast, as today's core decides: Google's QUOTA_EXHAUSTED (or a used-up credit
// balance), a rate limit that asks for a long wait, or a RESOURCE_EXHAUSTED reply with no reason
// that says the quota is exhausted.
func quotaUsedUp(g *googleError, raw []byte, waitMS int64) bool {
	if !strings.EqualFold(g.Error.Status, "RESOURCE_EXHAUSTED") {
		return false
	}
	switch g.reason() {
	case "QUOTA_EXHAUSTED", "INSUFFICIENT_G1_CREDITS_BALANCE":
		return true
	case "RATE_LIMIT_EXCEEDED":
		return waitMS >= longWait.Milliseconds()
	case "":
		low := strings.ToLower(string(raw))
		return strings.Contains(low, "quota_exhausted") || strings.Contains(low, "quota exhausted")
	}
	return false
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
