package main

import (
	"context"
	"crypto/rand"
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

// Kimi signs in with the OAuth 2.0 device authorization grant (RFC 8628) at its login host,
// {auth}/api/oauth/device_authorization and {auth}/api/oauth/token, as Kimi's own CLI does
// (MoonshotAI/kimi-code, packages/oauth). Kimi issues no client registration to other programs, so
// the login uses the public client of Kimi's CLI, as pluxy's Codex extension does with OpenAI's.
// Refresh is the refresh_token grant at the same token endpoint.

// clientID is Kimi CLI's public OAuth client.
const clientID = "17e5f671-d194-4dfb-9706-5516cb48c098"

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// defaultPollInterval is the wait between token polls when Kimi names none (RFC 8628 section
// 3.5).
const defaultPollInterval = 5 * time.Second

// refreshLead is how long before expiry core renews a token.
const refreshLead = 5 * time.Minute

var loginMethods = []protocol.LoginMethod{
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "Kimi Code API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "API key", Secret: true},
	}},
	{Method: "device", Kind: protocol.LoginDevice, Label: "Kimi Code (device code)"},
}

// authClient talks to Kimi's login. Its timeout covers only signing in and renewing, never a
// model request: a login host that stops answering must not hold a renewal, and every request of
// the account waiting on it, forever. It follows no redirect, which would send the form (a device
// code or refresh token) wherever the redirect points.
var authClient = &http.Client{
	Transport:     sdk.Upstream(http.DefaultTransport.(*http.Transport).Clone()),
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// prompter shows the user a login prompt; *sdk.LoginSession is one.
type prompter interface {
	Prompt(protocol.Prompt) error
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if call.Method == "api_key" {
		return apiKeyLogin(ctx, set, call.Input["api_key"])
	}
	if call.Method != "device" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
	}
	return deviceLogin(ctx, set, s)
}

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

func deviceLogin(ctx context.Context, set settings, s prompter) (*protocol.Grant, error) {
	deviceID := newDeviceID()
	var dc struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if f := oauthPost(ctx, set.AuthBaseURL+"/api/oauth/device_authorization", deviceID, url.Values{"client_id": {clientID}}, &dc); f != nil {
		return nil, requestError(ctx, f)
	}
	// The code's life and the poll interval are the server's numbers: bounded, so a huge one cannot
	// overflow a duration and a negative one cannot make a tight polling loop.
	lifetime := sdk.BoundSeconds(dc.ExpiresIn, sdk.MaxLoginWait)
	if err := s.Prompt(protocol.Prompt{
		Kind:                    protocol.PromptDevice,
		Message:                 "Open Kimi's page and enter the code to sign in to Kimi Code.",
		UserCode:                dc.UserCode,
		VerificationURI:         dc.VerificationURI,
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               int64(lifetime / time.Second),
	}); err != nil {
		return nil, err
	}
	interval := defaultPollInterval
	if d := sdk.BoundSeconds(dc.Interval, sdk.MaxPollInterval); d > 0 {
		interval = d
	}
	if lifetime > 0 {
		// Polling past the code's lifetime only earns expired_token; stop when it ends.
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Now().Add(lifetime))
		defer cancel()
	}
	expired := func(parent error) error {
		if errors.Is(parent, context.DeadlineExceeded) && lifetime > 0 {
			return refused("the code expired before it was approved; start the login again")
		}
		return parent
	}
	for {
		if !waitPoll(ctx, interval) {
			return nil, expired(ctx.Err())
		}
		var tr tokenResponse
		f := oauthPost(ctx, set.AuthBaseURL+"/api/oauth/token", deviceID, url.Values{
			"client_id": {clientID}, "device_code": {dc.DeviceCode}, "grant_type": {deviceGrant},
		}, &tr)
		if f == nil {
			return &protocol.Grant{Credential: tr.credential(deviceID, "")}, nil
		}
		if ctx.Err() != nil {
			return nil, expired(ctx.Err())
		}
		switch f.code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, refused("the sign-in was denied")
		case "expired_token":
			return nil, refused("the code expired before it was approved; start the login again")
		default:
			return nil, tokenError(f)
		}
	}
}

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
	_ = json.Unmarshal(c.Data, &data)
	var tr tokenResponse
	if f := oauthPost(ctx, set.AuthBaseURL+"/api/oauth/token", data.DeviceID, url.Values{
		"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken},
	}, &tr); f != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if f.status == http.StatusUnauthorized || f.status == http.StatusForbidden {
			// Kimi answers a refused refresh token with 401 or 403 rather than invalid_grant.
			return nil, &llm.Error{Code: llm.CodeAuth, Message: "Kimi refused the refresh token; log in again", Status: f.status, Scope: llm.ScopeCredential}
		}
		return nil, tokenError(f)
	}
	return tr.credential(data.DeviceID, c.RefreshToken), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// credential builds a credential from a token reply. A reply without a refresh token keeps
// oldRefresh: Kimi does not always rotate it.
func (tr *tokenResponse) credential(deviceID, oldRefresh string) *protocol.Credential {
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if c.RefreshToken == "" {
		c.RefreshToken = oldRefresh
	}
	if exp := sdk.ExpiryUnix(time.Now(), tr.ExpiresIn); exp > 0 {
		c.ExpiresAt = exp
		c.RefreshAt = c.ExpiresAt - int64(refreshLead/time.Second)
	}
	c.Data, _ = json.Marshal(credentialData{DeviceID: deviceID})
	return c
}

// newDeviceID returns a random UUID (version 4) naming this login's device to Kimi.
func newDeviceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// oauthFailure is a token or device reply that is not a success.
type oauthFailure struct {
	status       int
	code         string
	retryAfterMS int64
	err          error
	// sent is whether any of the request was written to the connection. Only a request that was
	// not sent is safe to try again.
	sent bool
}

// oauthPost posts form to endpoint with Kimi's device headers and decodes a 200 reply into out.
func oauthPost(ctx context.Context, endpoint, deviceID string, form url.Values, out any) *oauthFailure {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return &oauthFailure{err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	deviceHeaders(req.Header, deviceID)
	req, sent := sdk.TrackSend(req)
	resp, err := authClient.Do(req)
	if err != nil {
		return &oauthFailure{err: err, sent: sent()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return &oauthFailure{status: resp.StatusCode, code: sdk.LoginCode(e.Error), retryAfterMS: retryAfter(resp.Header.Get("Retry-After"), time.Now()), sent: true}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return &oauthFailure{status: resp.StatusCode, err: fmt.Errorf("the reply is not valid JSON: %w", err), sent: true}
	}
	return nil
}

func requestError(ctx context.Context, f *oauthFailure) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return tokenError(f)
}

// tokenError turns a failed token or device request into an error. Only a request that never
// reached Kimi is retryable, apart from a 429, where Kimi refused to run the request.
func tokenError(f *oauthFailure) error {
	switch {
	case f.status == 0:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("could not reach Kimi's login: %v", f.err), Retryable: !f.sent, Scope: llm.ScopeEndpoint}
	case f.err != nil:
		return &llm.Error{Code: llm.CodeUpstream, Message: f.err.Error(), Status: f.status, Scope: llm.ScopeEndpoint}
	case f.status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "Kimi's login is rate limiting", Status: f.status, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: f.retryAfterMS}
	case f.status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("Kimi's login answered %d", f.status), Status: f.status, Scope: llm.ScopeEndpoint, RetryAfterMS: f.retryAfterMS}
	case f.code == "invalid_grant":
		return &llm.Error{Code: llm.CodeAuth, Message: "Kimi refused the grant; log in again", Status: f.status, ProviderCode: f.code, Scope: llm.ScopeCredential}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("Kimi's login answered %d (%s)", f.status, f.code), Status: f.status, ProviderCode: f.code, Scope: llm.ScopeEndpoint}
}

func refused(msg string) error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

// apiKeyLogin lists models without generating, and retains only a key the API accepted.
func apiKeyLogin(ctx context.Context, set settings, key string) (*protocol.Grant, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "enter an API key", Scope: llm.ScopeRequest}
	}
	credential := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
	redact := protocol.NewRedactor(credential)
	clean := func(e *llm.Error) error {
		e = redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
		e.Message = sdk.LoginText(e.Message)
		e.ProviderCode = sdk.LoginCode(e.ProviderCode)
		// A login needs the refusal reason, not a raw body that may echo credentials.
		e.Raw = nil
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, clean(&llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url", Scope: llm.ScopeCredential})
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req, sent := sdk.TrackSend(req)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, clean(&llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the model list: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint})
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return &protocol.Grant{Credential: credential}, nil
	}
	e := upstreamError(resp, "")
	if e.Status >= 500 || e.Code == llm.CodeAuth || e.Code == llm.CodePermission {
		e.Retryable = false
	}
	return nil, clean(e)
}
