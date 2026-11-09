package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An xAI account signs in as the Grok CLI does and today's core does: the OAuth 2.0 device
// authorization grant (RFC 8628) with the Grok CLI's own public client, at the endpoints xAI's
// OpenID discovery document names. xAI registers no clients for other programs, so the login uses
// the CLI's, as pluxy's Codex and Kimi extensions use their apps' clients. The client has no
// secret. A logged-in account's chat requests go to the Grok CLI's chat endpoint
// (cli-chat-proxy.grok.com) with the CLI's identity (main.go).
const (
	clientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	defaultIssuer = "https://auth.x.ai"
	loginScope    = "openid profile email offline_access grok-cli:access api:access"
	deviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	// minPollInterval is the shortest wait between token polls, and the step slow_down adds;
	// maxPollDuration bounds the wait for the user. Both are today's core's.
	minPollInterval = 5 * time.Second
	maxPollDuration = 30 * time.Minute
	// refreshLead is how long before expiry core renews the token, as today's core does.
	refreshLead = 5 * time.Minute
)

// waitPoll waits d between token polls, and reports false when ctx ends first. Tests replace it.
var waitPoll = func(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var loginMethods = []protocol.LoginMethod{
	{Method: "device", Kind: protocol.LoginDevice, Label: "Grok account (device code)"},
}

// authClient talks to xAI's login endpoints. Its timeout covers only signing in and renewing,
// never a model request. It follows no redirect, which would send the form wherever it points.
var authClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// prompter shows the user a login prompt; *sdk.LoginSession is one.
type prompter interface {
	Prompt(protocol.Prompt) error
}

// credentialData is what a login's credential keeps besides its tokens.
type credentialData struct {
	// TokenEndpoint is the token endpoint discovery named at sign-in, which renewals use.
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if call.Method != "device" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
	}
	return deviceLogin(ctx, set, s)
}

type endpoints struct {
	device, token string
}

// discover reads the device authorization and token endpoints from the issuer's OpenID discovery
// document. Each must be HTTPS on x.ai, as today's core requires, or on the configured issuer's
// own scheme and host (a test server).
func discover(ctx context.Context, set settings) (endpoints, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.AuthIssuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return endpoints{}, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad auth_issuer: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Accept", "application/json")
	status, body, h, err := do(ctx, req)
	if err != nil {
		return endpoints{}, err
	}
	if status != http.StatusOK {
		return endpoints{}, loginFailure(status, body, h)
	}
	var doc struct {
		Device string `json:"device_authorization_endpoint"`
		Token  string `json:"token_endpoint"`
	}
	if json.Unmarshal(body, &doc) != nil || !allowedEndpoint(doc.Device, set.AuthIssuer) || !allowedEndpoint(doc.Token, set.AuthIssuer) {
		return endpoints{}, &llm.Error{Code: llm.CodeUpstream, Message: "xAI's discovery document names no usable device and token endpoints", Scope: llm.ScopeEndpoint}
	}
	return endpoints{device: doc.Device, token: doc.Token}, nil
}

// allowedEndpoint reports whether a login endpoint may receive the user's codes and tokens:
// HTTPS on x.ai or a subdomain, or the configured issuer's own scheme and host.
func allowedEndpoint(raw, issuer string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "https" && (host == "x.ai" || strings.HasSuffix(host, ".x.ai")) {
		return true
	}
	iu, err := url.Parse(issuer)
	return err == nil && iu.Host != "" && u.Scheme == iu.Scheme && strings.EqualFold(u.Host, iu.Host)
}

func deviceLogin(ctx context.Context, set settings, s prompter) (*protocol.Grant, error) {
	ep, err := discover(ctx, set)
	if err != nil {
		return nil, err
	}
	status, body, h, err := postForm(ctx, ep.device, url.Values{"client_id": {clientID}, "scope": {loginScope}})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, loginFailure(status, body, h)
	}
	var dc struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if json.Unmarshal(body, &dc) != nil || dc.DeviceCode == "" || dc.UserCode == "" || dc.VerificationURI == "" && dc.VerificationURIComplete == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "xAI's login sent no device code", Scope: llm.ScopeEndpoint}
	}
	// The code's life and the poll interval are the server's numbers: bounded, so a huge one cannot
	// overflow a duration and a negative one cannot make a tight polling loop.
	lifetime := sdk.BoundSeconds(dc.ExpiresIn, sdk.MaxLoginWait)
	verify := dc.VerificationURI
	if verify == "" {
		verify = dc.VerificationURIComplete
	}
	if err := s.Prompt(protocol.Prompt{
		Kind:                    protocol.PromptDevice,
		Message:                 "Open xAI's page and enter the code to sign in with your Grok account.",
		UserCode:                dc.UserCode,
		VerificationURI:         verify,
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               int64(lifetime / time.Second),
	}); err != nil {
		return nil, err
	}
	interval := minPollInterval
	if d := sdk.BoundSeconds(dc.Interval, sdk.MaxPollInterval); d > interval {
		interval = d
	}
	wait := maxPollDuration
	if lifetime > 0 && lifetime < wait {
		wait = lifetime
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ended := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return refused("the code expired before it was approved; start the login again")
		}
		return ctx.Err()
	}
	// The first poll goes at once, as today's core polls.
	for first := true; ; first = false {
		if !first && !waitPoll(ctx, interval) {
			return nil, ended()
		}
		status, body, h, err := postForm(ctx, ep.token, url.Values{"grant_type": {deviceGrant}, "device_code": {dc.DeviceCode}, "client_id": {clientID}})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ended()
			}
			// A poll that got no answer is tried again at the next interval.
			continue
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch e.Error {
		case "":
			if status == http.StatusOK {
				return grantFrom(body, ep.token, "")
			}
			if status == http.StatusTooManyRequests || status >= 500 {
				// An outage with no OAuth error leaves the device code good: poll again.
				continue
			}
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
		case "authorization_pending":
		case "slow_down":
			interval += minPollInterval
		case "access_denied":
			return nil, refused("the sign-in was denied")
		case "expired_token":
			return nil, refused("the code expired before it was approved; start the login again")
		default:
			// Core does not know the device code, so it cannot redact it.
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
		}
	}
}

// grantFrom builds the credential from a token reply. A reply without a refresh token keeps
// oldRefresh. The account is labelled with the e-mail address in the ID token, as today's core
// labels it, and its identity is the token's subject and e-mail address.
func grantFrom(body []byte, tokenEndpoint, oldRefresh string) (*protocol.Grant, error) {
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &t) != nil || t.AccessToken == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "xAI's token endpoint sent no access token", Scope: llm.ScopeEndpoint}
	}
	if t.RefreshToken == "" {
		t.RefreshToken = oldRefresh
	}
	data, _ := json.Marshal(credentialData{TokenEndpoint: tokenEndpoint})
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Data: data}
	if exp := sdk.ExpiryUnix(time.Now(), t.ExpiresIn); exp > 0 {
		c.ExpiresAt = exp
		c.RefreshAt = c.ExpiresAt - int64(refreshLead.Seconds())
	}
	claims := idTokenClaims(t.IDToken)
	return &protocol.Grant{Credential: c, Label: claims.Email, Identity: claims.identity()}, nil
}

// idClaims are the claims of an ID token that tell xAI accounts apart.
type idClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

// identity is who signed in, by the ID token's subject and e-mail address, so that core can refuse
// a new login to an account that signs in as someone else. Only the claims the token has.
func (c idClaims) identity() map[string]string {
	id := map[string]string{}
	if c.Subject != "" {
		id["sub"] = c.Subject
	}
	if c.Email != "" {
		id["email"] = c.Email
	}
	return id
}

// idTokenClaims reads the sub and email claims of an ID token. The token came straight from xAI's
// token endpoint over TLS, and the claims only label and tell apart the account, so its signature
// is not checked.
func idTokenClaims(tok string) idClaims {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return idClaims{}
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return idClaims{}
	}
	var claims idClaims
	_ = json.Unmarshal(b, &claims)
	return claims
}

// refresh renews a login's access token with its refresh token, at the token endpoint the login
// found, or found again when the credential's is missing or not one xAI's could be.
func refresh(ctx context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c == nil || c.Kind != protocol.CredentialOAuth || c.RefreshToken == "" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "this credential cannot be renewed", Scope: llm.ScopeRequest}
	}
	var data credentialData
	if len(c.Data) > 0 {
		_ = json.Unmarshal(c.Data, &data)
	}
	endpoint := data.TokenEndpoint
	if !allowedEndpoint(endpoint, set.AuthIssuer) {
		ep, err := discover(ctx, set)
		if err != nil {
			return nil, err
		}
		endpoint = ep.token
	}
	status, body, h, err := postForm(ctx, endpoint, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {c.RefreshToken}})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, loginFailure(status, body, h)
	}
	g, err := grantFrom(body, endpoint, c.RefreshToken)
	if err != nil {
		return nil, err
	}
	return g.Credential, nil
}

func postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad login endpoint: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return do(ctx, req)
}

func do(ctx context.Context, req *http.Request) (int, []byte, http.Header, error) {
	resp, err := authClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach xAI's login: " + err.Error(), Retryable: true, Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading xAI's login reply: " + err.Error(), Retryable: true, Scope: llm.ScopeEndpoint}
	}
	return resp.StatusCode, body, resp.Header, nil
}

// terminalGrantErrors are the refusals after which only a new login helps, as today's core reads
// them.
var terminalGrantErrors = []string{"invalid_grant", "invalid_client", "unauthorized_client", "refresh_token_reused",
	"refresh_token_expired", "refresh_token_invalidated", "token_revoked"}

// loginFailure turns a refusal from xAI's login into an error.
func loginFailure(status int, body []byte, h http.Header) *llm.Error {
	var b struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &b)
	// The code and the description are the provider's text, shown to the client before any
	// credential exists for core to redact: both are bounded and cleaned.
	code := sdk.LoginCode(b.Error)
	msg := fmt.Sprintf("xAI's login answered %d", status)
	if code != "" {
		msg += " (" + code + ")"
	}
	if d := sdk.LoginText(b.Description); d != "" {
		msg += ": " + d
	}
	terminal := false
	for _, t := range terminalGrantErrors {
		if b.Error == t || strings.Contains(b.Description, t) {
			terminal = true
		}
	}
	wait := retryAfter(h.Get("Retry-After"), time.Now())
	switch {
	case terminal || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &llm.Error{Code: llm.CodeAuth, Message: msg + "; log in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: msg, Status: status, ProviderCode: code, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: wait}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: msg, Status: status, Retryable: true, Scope: llm.ScopeEndpoint, RetryAfterMS: wait}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Status: status, ProviderCode: code, Scope: llm.ScopeEndpoint}
}

// hideSecrets returns err with each secret in its message replaced, for errors whose message
// carries text from xAI that may echo a code core does not know.
func hideSecrets(err *llm.Error, secrets ...string) *llm.Error {
	c := *err
	for _, s := range secrets {
		if s != "" {
			c.Message = strings.ReplaceAll(c.Message, s, "[redacted]")
		}
	}
	return &c
}

func refused(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}
