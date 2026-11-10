// Command claude is pluxy's provider extension for Anthropic's Claude: the Messages API with an
// API key, or with a Claude subscription signed in through OAuth.
//
// Account settings (all optional):
//
//	{"base_url": "https://api.anthropic.com"}
//
// base_url is where the Messages API lives; requests go to {base_url}/v1/messages. The OAuth
// client can be changed with client_id; tests move the sign-in page and token endpoint to a fake
// with authorize_url and token_url (sdk.TestLoginHost), which a normal run ignores; see oauth.go.
//
// Credentials: an api_key credential is sent in x-api-key, as Anthropic's API defines; an oauth
// credential (a subscription login) is sent as a bearer token, with the OAuth beta added to
// anthropic-beta. A setup token (the long-lived OAuth token `claude setup-token` prints) is kept
// as an api_key credential, since it is static and cannot be renewed, and is sent as a bearer
// token with the OAuth beta: any key containing sk-ant-oat is, as in today's core. A Messages request is sent unchanged, with the client's own Messages headers
// (API version, betas and its identification headers such as User-Agent and X-App), so thinking
// blocks, their signatures and cache_control markers reach Anthropic as the client wrote them.
// Requests in other formats (Chat Completions, Responses, Gemini) are translated to Messages,
// with the client's reasoning effort turned into Claude's thinking settings (thinking.go).
//
// Token counts (core's count_tokens call) go to Anthropic's count_tokens endpoint only when
// base_url is Anthropic's own origin (https://api.anthropic.com). For any other base_url the
// account answers that it cannot count, without a request, and core estimates the count itself:
// a Claude-compatible server may not have the endpoint, or may count differently.
//
// An account check (core's check call) of an api_key account reads Anthropic's model list,
// GET {base_url}/v1/models with x-api-key and anthropic-version, which costs the account nothing.
// A subscription login (oauth) and a setup token have no such call here, so they are left not
// checked, without a request.
//
// The extension never changes how a client presents itself: a client that is not Claude Code is
// not made to look like Claude Code.
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

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/claude"
	version = "0.1.0"
)

// defaultBaseURL is Anthropic's API.
const defaultBaseURL = "https://api.anthropic.com"

// anthropicVersion is the Messages API version sent when the client named none.
const anthropicVersion = "2023-06-01"

// oauthBeta is the beta Anthropic requires with an OAuth access token.
const oauthBeta = "oauth-2025-04-20"

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		CountTokens:  countTokens,
		Models:       listModels,
		Check:        check,
		Quota:        quota,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude:", err)
		os.Exit(1)
	}
}

// settings are an account's settings.
type settings struct {
	BaseURL      string `json:"base_url"`
	AuthorizeURL string `json:"authorize_url"`
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
}

// readSettings reads an account's settings and fills in the defaults.
func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	if s.BaseURL == "" {
		s.BaseURL = defaultBaseURL
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	if s.AuthorizeURL = sdk.TestLoginHost(s.AuthorizeURL); s.AuthorizeURL == "" {
		s.AuthorizeURL = defaultAuthorizeURL
	}
	if s.TokenURL = sdk.TestLoginHost(s.TokenURL); s.TokenURL == "" {
		s.TokenURL = defaultTokenURL
	}
	if s.ClientID == "" {
		s.ClientID = defaultClientID
	}
	return s, nil
}

// client has no timeouts after the connection is made: a long generation is not a failure, and
// cancellation comes from the call's context. It follows a redirect only to the same scheme and
// host, since Go keeps x-api-key on a redirect to another host.
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

// authorize sets the credential's headers on an upstream request.
func authorize(h http.Header, cred *protocol.Credential) error {
	switch {
	case cred == nil:
	case cred.Kind == protocol.CredentialAPIKey && isOAuthToken(cred.APIKey):
		// A setup token, kept as a key: it is an OAuth token, so it goes as one.
		bearer(h, cred.APIKey)
		return nil
	case cred.Kind == protocol.CredentialAPIKey && cred.APIKey != "":
		h.Set("X-Api-Key", cred.APIKey)
		return nil
	case cred.Kind == protocol.CredentialOAuth && cred.AccessToken != "":
		bearer(h, cred.AccessToken)
		return nil
	}
	return &llm.Error{Code: llm.CodeAuth, Message: "the account has no usable credential; add an API key or log in again", Scope: llm.ScopeCredential}
}

// bearer sets an OAuth access token on an upstream request: a bearer token, with the OAuth beta
// Anthropic requires added to anthropic-beta.
func bearer(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
	h.Set("Anthropic-Beta", withBeta(h.Get("Anthropic-Beta"), oauthBeta))
}

// isOAuthToken reports whether a key is a Claude OAuth access token rather than an API key. The
// long-lived token `claude setup-token` prints is one; Anthropic's access tokens contain
// sk-ant-oat (API keys sk-ant-api), which is the test today's core uses.
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

// withBeta adds beta to a comma-separated anthropic-beta value unless it is already there.
func withBeta(list, beta string) string {
	var out []string
	for _, b := range strings.Split(list, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	if !slices.Contains(out, beta) {
		out = append(out, beta)
	}
	return strings.Join(out, ",")
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	send := &reporting{Stream: out, tr: tr}
	resp, err := post(ctx, call, s, "/v1/messages", body, tr.Passthrough, call.Request.Stream)
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
	events, err := anthropicmessages.DecodeResponse(raw)
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

// post sends body to the Messages API at path (under base_url) and returns the upstream's
// successful reply; a failure is returned as the error to report. passthrough sends the client's
// own Messages headers with its body; stream asks for a stream. The betas the body needs are
// added (betas.go).
func post(ctx context.Context, call sdk.Call, s settings, path string, body []byte, passthrough, stream bool) (*http.Response, error) {
	body, betas := bodyBetas(body, path == "/v1/messages/count_tokens")
	// ?beta=true is how Anthropic's SDKs, Claude Code among them, call the API with betas.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+path+"?beta=true", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
		// An uncompressed stream reaches the client event by event.
		req.Header.Set("Accept-Encoding", "identity")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if passthrough {
		// The client's own Messages headers go with its body. Passthrough means the client
		// spoke Messages, so these are its headers, not ones made up for it.
		names := slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders)
		if anthropicmessages.IsClaudeCode(call.Request.Native.Headers) {
			names = append(names, anthropicmessages.ClaudeCodeHeaders...)
		}
		for _, h := range names {
			if v := call.Request.Native.Headers[h]; v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	if err := authorize(req.Header, call.Account.Credential); err != nil {
		return nil, err
	}
	if list := addBetas(req.Header.Get("Anthropic-Beta"), betas); list != "" {
		req.Header.Set("Anthropic-Beta", list)
	}
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
		return nil, upstreamError(resp, call.Request.Model)
	}
	return resp, nil
}

// check asks Anthropic for its model list with an API key, which costs nothing and generates
// nothing. A credential of another kind is answered unsupported without a request.
func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	cred := call.Account.Credential
	if cred != nil && cred.Kind != protocol.CredentialAPIKey {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only an API key can be checked without a generation; a login is not checked", Scope: llm.ScopeCredential}
	}
	if cred != nil && isOAuthToken(cred.APIKey) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "a setup token cannot be checked without a generation; it is not checked", Scope: llm.ScopeCredential}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if err := authorize(req.Header, cred); err != nil {
		return nil, err
	}
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
		Data []json.RawMessage `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	}
	return &protocol.CheckResult{Detail: fmt.Sprintf("the upstream lists %d models", len(list.Data))}, nil
}

// countTokens counts the request's input tokens with the Messages API's count_tokens. A
// Messages request is sent as it came, without the fields only generating uses; any other is
// rebuilt as Messages with only the fields count_tokens takes. An account whose base_url is not
// Anthropic's own origin cannot count: it answers so without a request.
func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	if !isAnthropicBase(s.BaseURL) {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "this account's base_url is not Anthropic's own (" + defaultBaseURL + "), which is the only one this extension sends a token count to"}
	}
	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	count := anthropicmessages.CountRequest
	if tr.Passthrough {
		count = anthropicmessages.CountBody
	}
	if body, err = count(body); err != nil {
		return err
	}
	resp, err := post(ctx, call, s, "/v1/messages/count_tokens", body, tr.Passthrough, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	n, err := anthropicmessages.DecodeCount(raw)
	if err != nil {
		return err
	}
	if err := out.Start(llm.ResponseInfo{Model: call.Request.Model, Format: llm.FormatAnthropicMessages}); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: &n, Raw: llm.RawJSON(raw)})
}

// isAnthropicBase reports whether baseURL is Anthropic's own API origin: https, the host
// api.anthropic.com, the port empty or 443, and no user info. Only that origin has the
// count_tokens endpoint this extension relies on (today's core draws the same line).
func isAnthropicBase(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.anthropic.com") {
		return false
	}
	port := u.Port()
	return port == "" || port == "443"
}

// buildBody returns the Messages request body for req and how it was built. A Messages request
// is sent unchanged; any other is rebuilt from the model, with its reasoning effort as thinking
// settings. A thinking suffix on the model name sets the thinking settings either way, in place
// of the request's own. Under the reject drop policy a translation that drops anything is
// refused.
func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: llm.FormatAnthropicMessages}
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == llm.FormatAnthropicMessages {
			tr.Passthrough = true
			body := n.Body
			if s := req.Options.ThinkingSuffix; s != "" {
				var err error
				if body, _, err = anthropicmessages.ApplyEffort(body, req.Model, s); err != nil {
					return nil, nil, err
				}
			}
			return body, tr, nil
		}
	}
	body, losses, err := anthropicmessages.EncodeRequest(req)
	if err != nil {
		return nil, nil, err
	}
	if body, err = addCacheBreakpoints(body); err != nil {
		return nil, nil, err
	}
	effort := req.Options.ReasoningEffort
	if s := req.Options.ThinkingSuffix; s != "" {
		effort = s
	}
	if effort != "" {
		var applied bool
		body, applied, err = anthropicmessages.ApplyEffort(body, req.Model, effort)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			// EncodeRequest reports the effort as dropped, leaving thinking to the provider.
			losses = slices.DeleteFunc(losses, func(l llm.Loss) bool { return l.Path == "reasoning_effort" })
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
			Message: "Anthropic Messages cannot carry parts of this request: " + strings.Join(paths, ", ")}
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

// relayStream decodes the upstream Messages stream and sends each event as soon as it is
// decoded. A blocked send blocks the next upstream read.
func relayStream(ctx context.Context, body io.Reader, out interface{ Send(llm.Event) error }) error {
	r := sse.NewReader(body)
	dec := anthropicmessages.NewStreamDecoder()
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

// upstreamError turns a non-2xx Messages response into a structured error. model is the model the
// request asked for.
func upstreamError(resp *http.Response, model string) *llm.Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	e := &llm.Error{Status: resp.StatusCode, Message: parsed.Error.Message, ProviderCode: parsed.Error.Type}
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
		// Anthropic names the model when it does not have it: then it did not run the request,
		// and another account may serve the model.
		e.Retryable = model != "" && strings.Contains(e.Message, model)
	case s == http.StatusTooManyRequests:
		// Only the model waits on this account, as today's core cools only the model that answered.
		e.Code, e.Scope, e.Retryable = llm.CodeRateLimit, llm.ScopeModel, true
		if model == "" {
			// A call for no model (the usage endpoint): the account is the one limited.
			e.Scope = llm.ScopeCredential
		}
		if strings.EqualFold(resp.Header.Get("Anthropic-Ratelimit-Unified-Status"), "rejected") {
			// A subscription's usage window is used up for every model; it opens again at the
			// unified reset.
			e.Code, e.Scope = llm.CodeQuota, llm.ScopeCredential
		}
		e.RetryAfterMS = rateLimitWait(resp.Header, time.Now())
	case s == http.StatusServiceUnavailable || s == 529:
		// 529 is Anthropic's overloaded_error: the request was refused, not run.
		e.Code, e.Scope, e.Retryable = llm.CodeUnavailable, llm.ScopeEndpoint, true
		e.RetryAfterMS = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	case s >= 500:
		// The upstream may have run the request before failing, so retrying is not known safe.
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	default:
		e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
	}
	return e
}

// rateLimitWait is how long to wait after a 429: Retry-After when given, otherwise the reset of a
// subscription's usage window that was refused (anthropic-ratelimit-unified-reset, Unix seconds).
func rateLimitWait(h http.Header, now time.Time) int64 {
	if ms := retryAfter(h.Get("Retry-After"), now); ms > 0 {
		return ms
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(h.Get("Anthropic-Ratelimit-Unified-Reset")), 10, 64); err == nil {
		if d := time.Unix(secs, 0).Sub(now); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
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
