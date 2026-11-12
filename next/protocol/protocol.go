// Package protocol holds the wire types and framing of the pluxy extension protocol. The
// protocol itself is described in README.md next to this file.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Version is the protocol version this package implements: the major version, which the
// handshake carries.
const Version = 0

// Minor is the minor version this package implements within Version. An extension reports it in
// describe (Info.ProtocolMinor); core uses what a minor version adds only with an extension that
// reports it. Minor 1 adds sessions; minor 2 adds token counting and the request's thinking
// suffix; minor 3 adds model listing; minor 4 adds account checks; minor 5 adds quota and the
// identity of a login's grant; minor 6 adds an error's quota window; minor 7 adds endpoint calls;
// minor 8 adds the request's caller; minor 9 adds a login grant's account settings;
// minor 10 adds the subscription's end time to quota; minor 11 adds the account's proxy_url;
// minor 12 adds the account's headers; minor 13 adds upstream capture (Call.Capture and the
// diagnostic frame).
const Minor = 13

// MaxFrame is the largest frame either side may send.
const MaxFrame = 64 << 20

// Environment variables core sets for an extension.
const (
	EnvProtocolVersions = "PLUXY_PROTOCOL_VERSIONS"
	EnvSocket           = "PLUXY_SOCKET"
	EnvExtensionID      = "PLUXY_EXTENSION_ID"
)

// EnvTestLoginHosts, set to 1 in core's environment, lets account settings move an extension's
// login and token endpoints to a test server. Only tests set it. Core passes it on to extensions
// when it is set; an extension honours such settings only when it holds it (sdk.TestLoginHost).
const EnvTestLoginHosts = "PLUXY_TEST_LOGIN_HOSTS"

// HandshakeMarker starts the handshake line.
const HandshakeMarker = "pluxy"

// Operations.
const (
	OpDescribe = "describe"
	OpGenerate = "generate"
	OpLogin    = "login"
	OpRefresh  = "refresh"
	// OpSession opens a session (minor version 1): one connection that carries turns, one at a
	// time, on one account.
	OpSession = "session"
	// OpCountTokens counts a request's input tokens (minor version 2), answered with events as
	// generate is: response.start, then finish whose usage holds the count.
	OpCountTokens = "count_tokens"
	// OpModels lists the models an account serves (minor version 3).
	OpModels = "models"
	// OpCheck asks whether an account works, without generating anything (minor version 4).
	OpCheck = "check"
	// OpQuota reads what an account's plan allows and how much of it is used (minor version 5).
	OpQuota = "quota"
	// OpEndpoint serves a client request to a provider endpoint beyond generation, such as
	// Responses compaction (minor version 7), answered with events: response.start, the reply's
	// body in native events, then finish.
	OpEndpoint = "endpoint"
)

// Frame types that are not events.
const (
	TypeCall           = "call"
	TypeDescribeResult = "describe.result"
	// TypeLoginInput is a frame core sends during a login call, after the call frame: what the
	// user entered for the extension's last prompt.
	TypeLoginInput = "login.input"
	// TypeLoginPrompt is a frame an extension sends during a login call: what to show the user.
	TypeLoginPrompt = "login.prompt"
	// TypeLoginDone ends a login call successfully, with the new credential.
	TypeLoginDone = "login.done"
	// TypeRefreshDone ends a refresh call successfully, with the renewed credential.
	TypeRefreshDone = "refresh.done"
	// TypeSessionReady is the extension's answer to a session call that it accepts.
	TypeSessionReady = "session.ready"
	// TypeSessionTurn is a frame core sends on a session: one request, answered with events.
	TypeSessionTurn = "session.turn"
	// TypeSessionInterrupt is a frame core sends on a session while a turn runs: the client's
	// control message for the running turn, in Raw.
	TypeSessionInterrupt = "session.interrupt"
	// TypeModelsResult answers a models call.
	TypeModelsResult = "models.result"
	// TypeCheckResult answers a check call for an account that works.
	TypeCheckResult = "check.result"
	// TypeQuotaResult answers a quota call.
	TypeQuotaResult = "quota.result"
	// TypeDiagnostic is a frame an extension sends before a captured call's terminal event: its
	// upstream exchanges (minor version 13). It is not an event.
	TypeDiagnostic = "diagnostic"
)

// Session kinds (SessionInfo.Format).
const (
	// SessionOpenAIResponses is a Responses WebSocket session: turns are Responses requests and
	// may continue the session's previous response with previous_response_id.
	SessionOpenAIResponses = llm.FormatOpenAIResponses
)

// SessionInfo says what kind of session a session call opens.
type SessionInfo struct {
	Format string `json:"format"`
}

// Call is the first frame core sends on a connection. For every op but login it is the only
// one; a login call may be followed by login.input frames.
type Call struct {
	Type    string       `json:"type"`
	ID      string       `json:"id"`
	Op      string       `json:"op"`
	Account *Account     `json:"account,omitempty"`
	Request *llm.Request `json:"request,omitempty"`
	// Login is what a login call asks for.
	Login *Login `json:"login,omitempty"`
	// Input is what the user entered, in a login.input frame.
	Input map[string]string `json:"input,omitempty"`
	// Session is what a session call opens.
	Session *SessionInfo `json:"session,omitempty"`
	// Raw is the client's control message, in a session.interrupt frame.
	Raw json.RawMessage `json:"raw,omitempty"`
	// Capture asks a generate, count_tokens or endpoint call, or a session.turn, to report its
	// upstream exchanges in a diagnostic frame before its terminal event (minor version 13).
	Capture bool `json:"capture,omitempty"`
}

// Login is the subject of a login call.
type Login struct {
	// Method is one of the login methods the extension's describe lists.
	Method string `json:"method"`
	// Input holds what the user entered when starting the login, by the method's input names
	// (for an api_key login, the key).
	Input map[string]string `json:"input,omitempty"`
}

// Account is the account a call runs under.
type Account struct {
	ID         string          `json:"id"`
	Settings   json.RawMessage `json:"settings,omitempty"`
	Credential *Credential     `json:"credential,omitempty"`
	// ProxyURL is the proxy every upstream request of the call goes through (minor version 11):
	// an http, https, socks5 or socks5h URL (ParseProxyURL). Empty: no proxy. Its user name and
	// password are secret.
	ProxyURL string `json:"proxy_url,omitempty"`
	// Headers are extra HTTP headers every upstream request of the call carries (minor version
	// 12), set over the extension's own headers of the same names. Values of headers whose names
	// are about authentication are secret.
	Headers map[string]string `json:"headers,omitempty"`
}

// ParseProxyURL parses an account's ProxyURL: an http, https, socks5 or socks5h URL with a
// host and nothing after it but an optional "/". Its error never quotes the URL, which may
// hold a password.
func ParseProxyURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, errors.New("is not a valid URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("must use http://, https://, socks5:// or socks5h://")
	}
	if u.Hostname() == "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be scheme://[user:password@]host[:port], with nothing after the port")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errors.New("has a port that is not a number from 1 to 65535")
		}
	}
	return u, nil
}

// SecretName reports whether a header or query parameter name is about authentication
// (Authorization, X-Api-Key, Cookie, key, ...), so its value is a secret.
func SecretName(name string) bool {
	n := strings.ToLower(name)
	for _, part := range []string{"auth", "key", "token", "secret", "cookie", "session", "password", "signature"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

// Credential kinds core knows. Extensions may use other kinds; core stores them as they are.
const (
	// CredentialAPIKey is a static key in APIKey. Core never refreshes it.
	CredentialAPIKey = "api_key"
	// CredentialOAuth is an OAuth grant: AccessToken, usually RefreshToken and ExpiresAt.
	CredentialOAuth = "oauth"
)

// Credential is the secret a call uses. Every string in it, except Kind, is treated as secret:
// core removes each from what a call sends back, so keep non-secret values out of it.
type Credential struct {
	Kind         string `json:"kind"`
	APIKey       string `json:"api_key,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is when AccessToken stops working, in Unix seconds; 0 means it does not
	// expire or the extension does not know.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// RefreshAt is when core should renew the credential, in Unix seconds. 0: 5 minutes
	// before ExpiresAt. Providers whose tokens are best renewed earlier (days ahead, say) set it.
	RefreshAt int64 `json:"refresh_at,omitempty"`
	// Data holds anything else the provider needs, as a JSON object (an ID token, a
	// service-account key, an account ID). Core stores and passes it on without reading it.
	Data json.RawMessage `json:"data,omitempty"`
}

// Info is the reply to describe.
type Info struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	// ProtocolMinor is the minor version of Protocol the extension implements; 0 when absent.
	ProtocolMinor int      `json:"protocol_minor,omitempty"`
	Operations    []string `json:"operations"`
	// LoginMethods are the ways login can add an account; present when Operations includes
	// login.
	LoginMethods []LoginMethod `json:"login_methods,omitempty"`
}

// Login method kinds.
const (
	// LoginAPIKey: the user enters a key; the extension checks it and returns a credential.
	LoginAPIKey = "api_key"
	// LoginBrowser: the user opens a URL and signs in; the extension gets the result on its own
	// callback or as a pasted redirect URL (OAuth authorization code with PKCE, RFC 7636).
	LoginBrowser = "browser"
	// LoginDevice: the user enters a code on another device while the extension polls (OAuth
	// device authorization grant, RFC 8628).
	LoginDevice = "device"
)

// LoginMethod is one way to log in.
type LoginMethod struct {
	// Method names it in a login call. Kind says how it works; an extension with two browser
	// logins (say, two account types) gives them different methods of the same kind.
	Method string `json:"method"`
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
	// Inputs are what the user enters when starting the login.
	Inputs []LoginInput `json:"inputs,omitempty"`
}

// LoginInput is one value the user enters.
type LoginInput struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Secret marks a value that must be hidden as it is typed and never shown again.
	Secret bool `json:"secret,omitempty"`
	// Optional marks a value the user may leave empty.
	Optional bool `json:"optional,omitempty"`
}

// Prompt kinds.
const (
	PromptBrowser = "browser"
	PromptDevice  = "device"
	PromptInput   = "input"
)

// Prompt is what a login asks the user to do.
type Prompt struct {
	// Kind is browser (open URL), device (open VerificationURI and enter UserCode) or input
	// (enter Inputs).
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
	// For device prompts (RFC 8628 section 3.2).
	UserCode                string `json:"user_code,omitempty"`
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	// ExpiresIn is how long the prompt stays valid, in seconds.
	ExpiresIn int64 `json:"expires_in,omitempty"`
	// Inputs the user may send back with login.input. A browser prompt usually accepts the
	// redirect URL, for when the extension's own callback cannot be reached.
	Inputs []LoginInput `json:"inputs,omitempty"`
}

// Grant is the result of a login or refresh.
type Grant struct {
	Credential *Credential `json:"credential"`
	// Settings supplies non-secret defaults for a new account (minor version 9). Explicit
	// caller properties win; a login to an existing account ignores these settings.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Label names the account for people, for example the e-mail address it belongs to. Not
	// secret; login only.
	Label string `json:"label,omitempty"`
	// Identity names the upstream account the credential signs in to, by the fields the
	// provider knows it by (for example "email", "account_id", "organization_id"), so core can
	// check that a new login to an existing account signs in to the same one (minor version 5).
	// Not secret; login only. At most MaxIdentityFields fields.
	Identity map[string]string `json:"identity,omitempty"`
}

// Bounds on a grant's Identity.
const (
	MaxIdentityFields = 16
	MaxIdentityKey    = 64
	MaxIdentityValue  = 512
)

// MaxGrantSettings bounds a login grant's encoded settings object to 8 KiB, the same
// budget as the maximum total identity value bytes (16 fields of 512 bytes).
const MaxGrantSettings = MaxIdentityFields * MaxIdentityValue

// ModelList is the result of a models call: the models an account serves.
type ModelList struct {
	// Models are the account's models, by the names its upstream knows them by. Empty when the
	// extension has no list for the account (it serves whatever model a client names).
	Models []Model `json:"models"`
	// OwnedBy names who provides the account's models, for a model list's owned_by: for models
	// the account's configuration names itself, and for those in Models that name none.
	OwnedBy string `json:"owned_by,omitempty"`
}

// Model is one model an account serves, with what model lists show about it. Only ID is
// required; every other field is shown where a client's model list has a place for it.
type Model struct {
	// ID is the model's name upstream, as requests name it.
	ID string `json:"id"`
	// Name is the model's resource name in Gemini's model list (for example
	// "models/gemini-2.5-pro"); default ID.
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Created is when the model was released, in Unix seconds.
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	// ContextLength is the model's context window in tokens, and MaxCompletionTokens its
	// longest reply.
	ContextLength       int `json:"context_length,omitempty"`
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// InputTokenLimit and OutputTokenLimit are Gemini's names for the same limits, where the
	// provider gives them apart; Gemini lists fall back to ContextLength and
	// MaxCompletionTokens.
	InputTokenLimit  int `json:"input_token_limit,omitempty"`
	OutputTokenLimit int `json:"output_token_limit,omitempty"`
	// GenerationMethods, InputModalities and OutputModalities are as Gemini's model list
	// names them (generateContent; TEXT, IMAGE, ...).
	GenerationMethods []string `json:"generation_methods,omitempty"`
	InputModalities   []string `json:"input_modalities,omitempty"`
	OutputModalities  []string `json:"output_modalities,omitempty"`
}

// CheckResult is the result of a check call for an account that works.
type CheckResult struct {
	// Detail says what was checked, for people. Not secret.
	Detail string `json:"detail,omitempty"`
}

// Quota is the result of a quota call: what an account's plan allows and how much of it is used,
// as the provider reports it.
type Quota struct {
	// Plan names the account's plan for people, as the provider names it.
	Plan string `json:"plan,omitempty"`
	// PlanEndsAt is when the subscription ends, in Unix seconds; zero means unknown (0.10).
	PlanEndsAt int64 `json:"plan_ends_at,omitempty"`
	// Limits are the plan's limits: usage windows, allowances and balances.
	Limits []QuotaLimit `json:"limits"`
	// Raw holds the provider's replies the quota was read from, as one JSON object, for what
	// Limits has no place for.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// MaxQuotaLimits bounds how many limits one quota call may return.
const MaxQuotaLimits = 1000

// QuotaLimit is one limit of an account's plan, such as a 5-hour usage window or a credit
// balance. Every amount is optional: a provider reports a share used, amounts, or both.
type QuotaLimit struct {
	// ID names the limit within the extension, stable across calls (for example "five_hour").
	ID string `json:"id"`
	// Label names it for people (for example "5-hour limit").
	Label string `json:"label,omitempty"`
	// UsedPercent is the share of the limit used, from 0 to 100.
	UsedPercent *float64 `json:"used_percent,omitempty"`
	// Used, Limit and Remaining are amounts in Unit.
	Used      *float64 `json:"used,omitempty"`
	Limit     *float64 `json:"limit,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
	// Unit is what the amounts count, for example "requests", "tokens", "credits" or "usd".
	Unit string `json:"unit,omitempty"`
	// Unlimited is true when the limit does not run out.
	Unlimited bool `json:"unlimited,omitempty"`
	// ResetsAt is when the limit resets, in Unix seconds.
	ResetsAt int64 `json:"resets_at,omitempty"`
	// WindowSeconds is the length of the limit's usage window.
	WindowSeconds int64 `json:"window_seconds,omitempty"`
	// Models are the models the limit applies to, when not all of the account's.
	Models []string `json:"models,omitempty"`
}

// Diagnostic is what a captured call reports about its upstream exchanges (minor version 13),
// for core's request log. Secrets are removed from it before it is sent.
type Diagnostic struct {
	// Upstream holds the call's exchanges in the order their requests were sent: at most
	// MaxDiagnosticExchanges, the first and the last when the call made more.
	Upstream []UpstreamExchange `json:"upstream,omitempty"`
	// UpstreamLeftOut counts the exchanges left out between them.
	UpstreamLeftOut int `json:"upstream_left_out,omitempty"`
}

// MaxDiagnosticExchanges is how many exchanges a diagnostic frame holds at most.
const MaxDiagnosticExchanges = 2

// Bounds on a captured body: one longer than CapturedHead+CapturedEnd bytes keeps its first
// CapturedHead bytes in Body and its last CapturedEnd bytes in BodyEnd.
const (
	CapturedHead = 32 << 10
	CapturedEnd  = 96 << 10
)

// CapturedHeaderBytes bounds the header lines of one captured request or response: each line
// counts its name, its value and 3 bytes (the ": " and the line end). Lines are taken in name
// order; one that does not fit is left out and counted in HeadersLeftOut.
const CapturedHeaderBytes = 16 << 10

// UpstreamExchange is one upstream request and what came back.
type UpstreamExchange struct {
	Request UpstreamRequest `json:"request"`
	// Response is absent when none came; Error then says why.
	Response *UpstreamResponse `json:"response,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// UpstreamRequest is an upstream request as it was sent.
type UpstreamRequest struct {
	// TimeMs is when it was sent, in Unix milliseconds.
	TimeMs int64  `json:"time_ms,omitempty"`
	Method string `json:"method"`
	// URL is the whole URL, without user name and password, secret query values redacted.
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	// HeadersLeftOut counts the header lines left out of Headers (CapturedHeaderBytes).
	HeadersLeftOut int `json:"headers_left_out,omitempty"`
	CapturedBody
}

// UpstreamResponse is what an upstream request got back.
type UpstreamResponse struct {
	// TimeMs is when its headers arrived, in Unix milliseconds.
	TimeMs  int64               `json:"time_ms,omitempty"`
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	// HeadersLeftOut counts the header lines left out of Headers (CapturedHeaderBytes).
	HeadersLeftOut int `json:"headers_left_out,omitempty"`
	CapturedBody
}

// CapturedBody is a body as text, bounded to its start and its end.
type CapturedBody struct {
	// Body is all of the body, or its first CapturedHead bytes when it is longer than
	// CapturedHead+CapturedEnd.
	Body string `json:"body,omitempty"`
	// BodyEnd is the body's last CapturedEnd bytes when Body holds only its start.
	BodyEnd string `json:"body_end,omitempty"`
	// BodySize is the body's full length in bytes; for a response, as much as had been read.
	BodySize int64 `json:"body_size,omitempty"`
}

// Reply is a frame an extension sends: an event, the result of describe, models, check or quota,
// a login or refresh frame, or a diagnostic frame.
type Reply struct {
	llm.Event
	Info       *Info        `json:"info,omitempty"`
	Prompt     *Prompt      `json:"prompt,omitempty"`
	Grant      *Grant       `json:"grant,omitempty"`
	Models     *ModelList   `json:"models,omitempty"`
	Check      *CheckResult `json:"check,omitempty"`
	Quota      *Quota       `json:"quota,omitempty"`
	Diagnostic *Diagnostic  `json:"diagnostic,omitempty"`
}

// ErrFrameTooLarge is returned for a frame longer than MaxFrame.
var ErrFrameTooLarge = errors.New("protocol: frame too large")

// WriteFrame writes v as one frame. It returns ErrFrameTooLarge, having written nothing, when v
// encodes to more than MaxFrame bytes.
func WriteFrame(w io.Writer, v any) error {
	// The header goes first in the same buffer, so the frame is encoded once and not copied.
	var buf bytes.Buffer
	buf.Write(make([]byte, 4))
	enc := json.NewEncoder(&buf)
	// No HTML escaping: it would turn each "<", ">" and "&" in the request or reply into six
	// bytes, and the frame goes to a program, not a web page.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b := buf.Bytes()
	b = b[:len(b)-1] // Encode ends the value with a newline
	n := len(b) - 4
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	binary.BigEndian.PutUint32(b, uint32(n))
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame into v. It returns io.EOF only when the stream ends cleanly between
// frames.
func ReadFrame(r io.Reader, v any) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return fmt.Errorf("protocol: truncated frame header: %w", err)
		}
		return err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("protocol: truncated frame: %w", err)
	}
	return json.Unmarshal(body, v)
}

// HandshakeLine returns the line an extension prints once it listens.
func HandshakeLine(version int, socket string) string {
	return fmt.Sprintf("%s|%d|unix|%s\n", HandshakeMarker, version, socket)
}

// ParseHandshake parses a handshake line (without its newline).
func ParseHandshake(line string) (version int, network, address string, err error) {
	parts := strings.SplitN(strings.TrimRight(line, "\r\n"), "|", 4)
	if len(parts) != 4 || parts[0] != HandshakeMarker {
		return 0, "", "", fmt.Errorf("protocol: not a handshake line: %q", line)
	}
	version, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", "", fmt.Errorf("protocol: bad version in handshake line: %q", line)
	}
	if parts[2] != "unix" || parts[3] == "" {
		return 0, "", "", fmt.Errorf("protocol: unsupported network in handshake line: %q", line)
	}
	return version, parts[2], parts[3], nil
}

// ParseVersions parses PLUXY_PROTOCOL_VERSIONS.
func ParseVersions(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("protocol: bad version %q", f)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("protocol: no versions")
	}
	return out, nil
}
