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

const (
	clientID        = "b1a00492-073a-47ea-816f-4c329264a828"
	defaultIssuer   = "https://auth.x.ai"
	loginScope      = "openid profile email offline_access grok-cli:access api:access"
	deviceGrant     = "urn:ietf:params:oauth:grant-type:device_code"
	minPollInterval = 5 * time.Second
	maxPollDuration = 30 * time.Minute
	refreshLead     = 5 * time.Minute
)

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
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "xAI API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "API key", Secret: true},
	}},
	{Method: "device", Kind: protocol.LoginDevice, Label: "Grok account (device code)"},
}

var authClient = &http.Client{
	Transport:     sdk.Upstream(http.DefaultTransport.(*http.Transport).Clone()),
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type prompter interface {
	Prompt(protocol.Prompt) error
}

type credentialData struct {
	TokenEndpoint string `json:"token_endpoint,omitempty"`
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
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
	}
	return deviceLogin(ctx, set, s)
}

type endpoints struct {
	device, token string
}

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
	for first := true; ; first = false {
		if !first && !waitPoll(ctx, interval) {
			return nil, ended()
		}
		status, body, h, err := postForm(ctx, ep.token, url.Values{"grant_type": {deviceGrant}, "device_code": {dc.DeviceCode}, "client_id": {clientID}})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ended()
			}
			var le *llm.Error
			if errors.As(err, &le) && le.Retryable {
				continue
			}
			return nil, err
		}
		if status >= 500 {
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
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
			if status == http.StatusTooManyRequests {
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
			if status == http.StatusTooManyRequests {
				// A 429 refused the poll whatever error text it carries (xAI's own say
				// {"error": "Rate limit reached"}): poll again.
				continue
			}
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
		}
	}
}

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

type idClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

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
	req, sent := sdk.TrackSend(req)
	resp, err := authClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach xAI's login: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading xAI's login reply: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	return resp.StatusCode, body, resp.Header, nil
}

var terminalGrantErrors = []string{"invalid_grant", "invalid_client", "unauthorized_client", "refresh_token_reused",
	"refresh_token_expired", "refresh_token_invalidated", "token_revoked"}

func loginFailure(status int, body []byte, h http.Header) *llm.Error {
	var b struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &b)
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
	wait := sdk.RetryAfter(h.Get("Retry-After"), time.Now())
	switch {
	case terminal || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &llm.Error{Code: llm.CodeAuth, Message: msg + "; log in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: msg, Status: status, ProviderCode: code, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: wait}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: msg, Status: status, Scope: llm.ScopeEndpoint, RetryAfterMS: wait}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Status: status, ProviderCode: code, Scope: llm.ScopeEndpoint}
}

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
		e.Raw = nil
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/models", nil)
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
