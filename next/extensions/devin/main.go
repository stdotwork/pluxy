// Command devin is pluxy's provider extension for Devin (Cognition's coding agent, formerly
// Windsurf's Codeium): the models a Devin account gets, through Devin's API server, signed in as
// Devin's CLI signs in (login.go) or with a session token.
//
// Account settings (all optional):
//
//	{"base_url": "https://server.codeium.com", "device_seed": "..."}
//
// base_url is Devin's API server; chat requests go to
// {base_url}/exa.api_server_pb.ApiServerService/GetChatMessage. device_seed makes the device
// fingerprint each request carries the same every time; without it the fingerprint is random, as
// today's core sends it. app_base_url and api_base_url move Devin's sign-in page and login API
// to a test server; callback_listen false keeps the login from listening for the browser's
// return.
//
// Every client format is sent as Devin's protobuf chat request over Connect (request.go,
// wire.go), always streamed, and the reply's frames become the reply's events.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/devin"
	version = "0.1.0"

	defaultBaseURL    = "https://server.codeium.com"
	defaultAppBaseURL = "https://app.devin.ai"
	defaultAPIBaseURL = "https://api.devin.ai"
	chatPath          = "/exa.api_server_pb.ApiServerService/GetChatMessage"

	// opaqueFormat names Devin's reasoning signatures in a reply's reasoning items.
	opaqueFormat = "devin"
)

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "devin:", err)
		os.Exit(1)
	}
}

type settings struct {
	BaseURL        string `json:"base_url"`
	DeviceSeed     string `json:"device_seed"`
	AppBaseURL     string `json:"app_base_url"`
	APIBaseURL     string `json:"api_base_url"`
	CallbackListen *bool  `json:"callback_listen"`
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	trim := func(v, def string) string {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v == "" {
			return def
		}
		return v
	}
	s.BaseURL = trim(s.BaseURL, defaultBaseURL)
	s.AppBaseURL = trim(s.AppBaseURL, defaultAppBaseURL)
	s.APIBaseURL = trim(s.APIBaseURL, defaultAPIBaseURL)
	return s, nil
}

// sessionToken returns the account's Devin session token: a credential's API key, or a login's
// access token.
func sessionToken(c *protocol.Credential) string {
	if c == nil {
		return ""
	}
	if c.Kind == protocol.CredentialAPIKey && c.APIKey != "" {
		return formatToken(c.APIKey)
	}
	if c.Kind == protocol.CredentialOAuth && c.AccessToken != "" {
		return formatToken(c.AccessToken)
	}
	return ""
}

// tokenPrefix starts a Devin session token. A token given as the bare JWT inside it gets the
// prefix, as today's core reads it.
const tokenPrefix = "devin-session-token$"

func formatToken(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "eyJ") {
		return tokenPrefix + t
	}
	return t
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It does not ask for compressed replies, as Devin's
// CLI does not, and follows a redirect only to the same scheme and host, since the redirect
// would carry the session token.
var client = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
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

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error { return chat(ctx, call, out) }

// chat sends a request to Devin and its reply's events to out.
func chat(ctx context.Context, call sdk.Call, out sender) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	token := sessionToken(call.Account.Credential)
	if token == "" {
		return &llm.Error{Code: llm.CodeAuth, Message: "the account has no Devin session token", Scope: llm.ScopeCredential}
	}
	req := &call.Request
	chat := buildRequest(req)
	tr := &llm.Translation{To: "devin.chat", Losses: chat.losses}
	if req.Origin != nil {
		tr.From = req.Origin.Format
	}
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		var paths []string
		for _, l := range chat.losses {
			if l.Kind == llm.LossDropped {
				paths = append(paths, l.Path)
			}
		}
		return &llm.Error{Code: llm.CodeUnsupported, Message: "Devin has no place for " + strings.Join(paths, ", "), Scope: llm.ScopeRequest}
	}
	body := frame(chat.encode(token, deviceFingerprint(s.DeviceSeed)))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+chatPath, bytes.NewReader(body))
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(hreq.Header, token)
	hreq.Header.Set("Content-Type", "application/connect+proto")
	hreq.Header.Set("Sentry-Trace", sentryTrace())

	resp, err := client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		// A failed dial never reached Devin, so trying again is safe.
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return statusError(resp.StatusCode, connectMessage(raw), raw, resp.Header.Get("Retry-After"))
	}
	r := &relay{out: out, model: req.Model, tr: tr}
	return r.run(ctx, resp.Body)
}

// setHeaders sets the headers Devin's CLI sends with every call: the session token twice as
// Basic credentials, Connect's version, and no User-Agent.
func setHeaders(h http.Header, token string) {
	h.Set("Authorization", "Basic "+token+"-"+token)
	h.Set("Connect-Protocol-Version", "1")
	h.Set("Accept", "*/*")
	// An empty User-Agent keeps Go from sending its own, as the CLI sends none.
	h["User-Agent"] = []string{""}
}

// sentryTrace returns a new Sentry trace header (trace ID, span ID, sampled), as the CLI sends.
func sentryTrace() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:]) + "-1"
}

// sender takes the reply's events; *sdk.Stream is one.
type sender interface{ Send(llm.Event) error }

// relay turns the reply's Connect frames into events as they arrive. Each data frame carries
// pieces of reasoning, text and tool calls; a new kind of piece closes the item before it.
type relay struct {
	out   sender
	model string
	tr    *llm.Translation

	started  bool // response.start was sent
	next     int  // index of the next item
	open     string
	openIdx  int
	openCall string
	sig      []byte
	calls    int
	stop     uint64
	usage    usage
}

type usage struct{ input, output, cacheWrite, cacheRead int64 }

func (r *relay) send(ev llm.Event) error {
	if !r.started {
		r.started = true
		info := &llm.ResponseInfo{ID: "devin-" + newUUID(), Model: r.model, Created: time.Now().Unix(), Translation: r.tr}
		if err := r.out.Send(llm.Event{Type: llm.EventResponseStart, Response: info}); err != nil {
			return err
		}
	}
	return r.out.Send(ev)
}

// item makes sure an item of kind is open, closing the one before it, and returns its index.
// A tool call with a different ID is a new item.
func (r *relay) item(kind, callID, name string) (int, error) {
	if r.open == kind && (kind != llm.ItemToolCall || callID == "" || callID == r.openCall) {
		return r.openIdx, nil
	}
	if err := r.close(); err != nil {
		return 0, err
	}
	r.open, r.openIdx, r.openCall = kind, r.next, callID
	r.next++
	if kind == llm.ItemToolCall {
		r.calls++
	}
	return r.openIdx, r.send(llm.Event{Type: llm.EventItemStart, Index: r.openIdx, Item: &llm.ItemStart{Kind: kind, CallID: callID, Name: name}})
}

func (r *relay) close() error {
	if r.open == "" {
		return nil
	}
	ev := llm.Event{Type: llm.EventItemEnd, Index: r.openIdx}
	if r.open == llm.ItemReasoning && len(r.sig) > 0 {
		ev.Opaque = &llm.Opaque{Format: opaqueFormat, Data: string(r.sig)}
	}
	r.open, r.sig = "", nil
	return r.send(ev)
}

func (r *relay) run(ctx context.Context, body io.Reader) error {
	for {
		end, payload, err := readFrame(body)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			msg := "Devin's reply ended before its end frame"
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				msg = "reading Devin's reply: " + err.Error()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: msg, Scope: llm.ScopeEndpoint, Usage: r.usage.llm()}
		}
		if end {
			if e := trailerError(payload); e != nil {
				// Once the reply began, the model ran: trying again is not safe.
				e.Retryable = e.Retryable && !r.started
				e.Usage = r.usage.llm()
				return e
			}
			return r.finish()
		}
		if err := r.data(payload); err != nil {
			return err
		}
	}
}

// data handles one data frame. A frame Devin's format does not allow is skipped, as today's core
// skips it.
func (r *relay) data(payload []byte) error {
	fields, err := parseFields(payload)
	if err != nil {
		return nil
	}
	var text, thinking strings.Builder
	var calls [][]byte
	for _, f := range fields {
		switch {
		case f.num == 3 && f.wire == wireBytes:
			text.Write(f.data)
		case f.num == 9 && f.wire == wireBytes:
			thinking.Write(f.data)
		case f.num == 10 && f.wire == wireBytes:
			r.sig = append(r.sig, f.data...)
		case f.num == 6 && f.wire == wireBytes:
			calls = append(calls, f.data)
		case f.num == 5 && f.wire == wireVarint:
			r.stop = f.val
		case f.num == 7 && f.wire == wireBytes:
			r.usage.merge(f.data)
		}
	}
	if thinking.Len() > 0 {
		sig := r.sig
		idx, err := r.item(llm.ItemReasoning, "", "")
		if err != nil {
			return err
		}
		r.sig = sig
		if err := r.send(llm.Event{Type: llm.EventReasoningDelta, Index: idx, Delta: thinking.String()}); err != nil {
			return err
		}
	}
	if text.Len() > 0 {
		idx, err := r.item(llm.ItemMessage, "", "")
		if err != nil {
			return err
		}
		if err := r.send(llm.Event{Type: llm.EventTextDelta, Index: idx, Delta: text.String()}); err != nil {
			return err
		}
	}
	for _, c := range calls {
		if err := r.toolCall(c); err != nil {
			return err
		}
	}
	return nil
}

// toolCall handles a piece of a tool call: its ID and name come first, then its arguments in
// pieces.
func (r *relay) toolCall(b []byte) error {
	fields, err := parseFields(b)
	if err != nil {
		return nil
	}
	var callID, name, args string
	for _, f := range fields {
		if f.wire != wireBytes {
			continue
		}
		switch f.num {
		case 1:
			callID = string(f.data)
		case 2:
			name = string(f.data)
		case 3:
			args += string(f.data)
		}
	}
	if r.open != llm.ItemToolCall && callID == "" {
		callID = "call_" + strings.ReplaceAll(newUUID(), "-", "")[:24]
	}
	idx, err := r.item(llm.ItemToolCall, callID, name)
	if err != nil {
		return err
	}
	if args == "" {
		return nil
	}
	return r.send(llm.Event{Type: llm.EventToolInputDelta, Index: idx, Delta: args})
}

// Devin's stop reasons that end a reply early.
const (
	stopIncomplete    = 1
	stopMaxTokens     = 3
	stopContentFilter = 11
)

func (r *relay) finish() error {
	if err := r.close(); err != nil {
		return err
	}
	f := &llm.Finish{Reason: llm.FinishStop}
	switch r.stop {
	case stopIncomplete, stopMaxTokens:
		f.Reason = llm.FinishLength
	case stopContentFilter:
		f.Reason = llm.FinishContentFilter
	default:
		if r.calls > 0 {
			f.Reason = llm.FinishToolCalls
		}
	}
	if r.stop != 0 {
		f.RawReason = strconv.FormatUint(r.stop, 10)
	}
	return r.send(llm.Event{Type: llm.EventFinish, Finish: f, Usage: r.usage.llm()})
}

// merge adds a usage message: 2 input tokens not read from a cache, 3 output tokens, 4 tokens
// written to a cache, 5 read from one. A later non-zero count replaces an earlier one.
func (u *usage) merge(b []byte) {
	fields, err := parseFields(b)
	if err != nil {
		return
	}
	for _, f := range fields {
		if f.wire != wireVarint || f.val == 0 {
			continue
		}
		switch f.num {
		case 2:
			u.input = int64(f.val)
		case 3:
			u.output = int64(f.val)
		case 4:
			u.cacheWrite = int64(f.val)
		case 5:
			u.cacheRead = int64(f.val)
		}
	}
}

func (u usage) llm() *llm.Usage {
	if u == (usage{}) {
		return nil
	}
	in, out := u.input+u.cacheRead+u.cacheWrite, u.output
	l := &llm.Usage{InputTokens: &in, OutputTokens: &out}
	if u.cacheRead > 0 {
		l.CachedInputTokens = &u.cacheRead
	}
	if u.cacheWrite > 0 {
		l.CacheWriteInputTokens = &u.cacheWrite
	}
	return l
}

// trailerError returns the error an end frame carries ({"error": {"code", "message"}}), or nil
// for a clean end. An end frame that is not such JSON is a clean end, as today's core reads it.
func trailerError(payload []byte) *llm.Error {
	var t struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &t) != nil || t.Error == nil || t.Error.Code == "" && t.Error.Message == "" {
		return nil
	}
	e := statusError(connectStatus(t.Error.Code, t.Error.Message), t.Error.Message, payload, "")
	e.ProviderCode = t.Error.Code
	return e
}

// connectStatus maps a Connect error code to the HTTP status it means, as today's core maps it:
// a few codes depend on the message, which says whether a refusal is about quota or demand.
func connectStatus(code, msg string) int {
	m := strings.ToLower(msg)
	switch code {
	case "invalid_argument":
		if strings.Contains(m, "internal error") {
			return http.StatusBadGateway
		}
		return http.StatusBadRequest
	case "unauthenticated":
		return http.StatusUnauthorized
	case "permission_denied":
		if strings.Contains(m, "high demand") {
			return http.StatusTooManyRequests
		}
		return http.StatusForbidden
	case "resource_exhausted":
		return http.StatusTooManyRequests
	case "unavailable":
		return http.StatusServiceUnavailable
	case "deadline_exceeded":
		return http.StatusGatewayTimeout
	case "failed_precondition":
		if containsAny(m, "quota", "credit", "acu", "exhausted", "limit") {
			return http.StatusTooManyRequests
		}
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// connectMessage returns the message of a Connect error body ({"code", "message"}), or the body.
func connectMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(raw))
}

// statusError classifies a refusal by its HTTP status. A 429 holds back only the model, as
// today's core cools only the model, unless Devin says the account's daily or weekly quota is
// used up.
func statusError(status int, msg string, raw []byte, retryAfterHeader string) *llm.Error {
	if msg == "" {
		msg = fmt.Sprintf("Devin answered %d %s", status, http.StatusText(status))
	}
	e := &llm.Error{Status: status, Message: msg}
	if json.Valid(raw) {
		e.Raw = raw
	}
	switch {
	case status == http.StatusUnauthorized:
		e.Code, e.Scope, e.Retryable = llm.CodeAuth, llm.ScopeCredential, true
	case status == http.StatusForbidden:
		e.Code, e.Scope, e.Retryable = llm.CodePermission, llm.ScopeCredential, true
	case status == http.StatusNotFound:
		e.Code, e.Scope = llm.CodeNotFound, llm.ScopeModel
	case status == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		e.RetryAfterMS = retryAfter(retryAfterHeader, time.Now())
		if m := strings.ToLower(msg); strings.Contains(m, "quota") && strings.Contains(m, "exhausted") {
			e.Code, e.Scope = llm.CodeQuota, llm.ScopeCredential
		}
	case status == http.StatusServiceUnavailable:
		// Not retry-safe: Devin may have started the request.
		e.Code, e.Scope = llm.CodeUnavailable, llm.ScopeEndpoint
		e.RetryAfterMS = retryAfter(retryAfterHeader, time.Now())
	case status >= 500:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	case status >= 300 && status < 400:
		e.Code, e.Scope, e.Status = llm.CodeUpstream, llm.ScopeEndpoint, http.StatusBadGateway
		e.Message = fmt.Sprintf("Devin redirected (%d) to another host; not followed", status)
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// retryAfter reads a Retry-After header (seconds or an HTTP date) as milliseconds from now.
func retryAfter(v string, now time.Time) int64 {
	if v = strings.TrimSpace(v); v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		return secs * 1000
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
}
