package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	{Method: "device", Kind: protocol.LoginDevice, Label: "Kimi Code (device code)"},
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if call.Method != "device" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
	}
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
	if err := s.Prompt(protocol.Prompt{
		Kind:                    protocol.PromptDevice,
		Message:                 "Open Kimi's page and enter the code to sign in to Kimi Code.",
		UserCode:                dc.UserCode,
		VerificationURI:         dc.VerificationURI,
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               dc.ExpiresIn,
	}); err != nil {
		return nil, err
	}
	interval := defaultPollInterval
	if dc.Interval > 0 {
		interval = time.Duration(dc.Interval) * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		var tr tokenResponse
		f := oauthPost(ctx, set.AuthBaseURL+"/api/oauth/token", deviceID, url.Values{
			"client_id": {clientID}, "device_code": {dc.DeviceCode}, "grant_type": {deviceGrant},
		}, &tr)
		if f == nil {
			return &protocol.Grant{Credential: tr.credential(deviceID, "")}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
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
	if tr.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Unix() + tr.ExpiresIn
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
	resp, err := client.Do(req)
	if err != nil {
		return &oauthFailure{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		var wait int64
		if secs, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && secs > 0 {
			wait = secs * 1000
		}
		return &oauthFailure{status: resp.StatusCode, code: e.Error, retryAfterMS: wait}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return &oauthFailure{status: resp.StatusCode, err: fmt.Errorf("the reply is not valid JSON: %w", err)}
	}
	return nil
}

func requestError(ctx context.Context, f *oauthFailure) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return tokenError(f)
}

// tokenError turns a failed token or device request into an error.
func tokenError(f *oauthFailure) error {
	switch {
	case f.status == 0:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("could not reach Kimi's login: %v", f.err), Retryable: true, Scope: llm.ScopeEndpoint}
	case f.err != nil:
		return &llm.Error{Code: llm.CodeUpstream, Message: f.err.Error(), Status: f.status, Scope: llm.ScopeEndpoint}
	case f.status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "Kimi's login is rate limiting", Status: f.status, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: f.retryAfterMS}
	case f.status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("Kimi's login answered %d", f.status), Status: f.status, Retryable: true, Scope: llm.ScopeEndpoint}
	case f.code == "invalid_grant":
		return &llm.Error{Code: llm.CodeAuth, Message: "Kimi refused the grant; log in again", Status: f.status, ProviderCode: f.code, Scope: llm.ScopeCredential}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("Kimi's login answered %d (%s)", f.status, f.code), Status: f.status, ProviderCode: f.code, Scope: llm.ScopeEndpoint}
}

func refused(msg string) error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}
