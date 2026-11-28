package main

import (
	"bytes"
	"context"
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
	clientID            = "1031625952748946"
	defaultAuthBaseURL  = "https://auth.meta.com"
	defaultMintURL      = "https://api.meta.ai/muse-code/key"
	deviceGrant         = "urn:ietf:params:oauth:grant-type:device_code"
	loginUserAgent      = "muse-code/1.0.2"
	defaultPollInterval = 5 * time.Second
	maxPollDuration     = 15 * time.Minute
)

var loginMethods = []protocol.LoginMethod{
	{Method: "device", Kind: protocol.LoginDevice, Label: "Muse (Meta) (device code)"},
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "Meta API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "API key", Secret: true},
	}},
}

var authClient = &http.Client{
	Transport:     sdk.Upstream(http.DefaultTransport.(*http.Transport).Clone()),
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type prompter interface {
	Prompt(protocol.Prompt) error
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "device":
		return deviceLogin(ctx, set, s)
	case "api_key":
		key := strings.TrimSpace(call.Input["api_key"])
		if key == "" {
			return nil, refused("enter an API key")
		}
		if strings.HasPrefix(key, "dca:") {
			return nil, refused("a dca: token is a Muse Code login token, not an API key; sign in with the device code instead")
		}
		return &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}, nil
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
}

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
	var dc struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	status, body, h, err := postForm(ctx, set.AuthBaseURL+"/oidc/device/authorization/", url.Values{"client_id": {clientID}})
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if status < 200 || status > 299 {
		return nil, loginFailure(status, body, h)
	}
	if json.Unmarshal(body, &dc) != nil || dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Meta's login sent no device code", Scope: llm.ScopeEndpoint}
	}
	lifetime := sdk.BoundSeconds(dc.ExpiresIn, sdk.MaxLoginWait)
	if err := s.Prompt(protocol.Prompt{
		Kind:                    protocol.PromptDevice,
		Message:                 "Open Meta's page and enter the code to sign in to Muse Code.",
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
	wait := maxPollDuration
	if lifetime > 0 && lifetime < wait {
		wait = lifetime
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	expired := func(parent error) error {
		if errors.Is(parent, context.DeadlineExceeded) {
			return refused("the code expired before it was approved; start the login again")
		}
		return parent
	}
	for {
		if !waitPoll(ctx, interval) {
			return nil, expired(ctx.Err())
		}
		status, body, h, err := postForm(ctx, set.AuthBaseURL+"/oidc/device/token/", url.Values{
			"grant_type": {deviceGrant}, "device_code": {dc.DeviceCode}, "client_id": {clientID},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, expired(ctx.Err())
			}
			if wasSent(err) {
				return nil, requestError(ctx, err)
			}
			continue
		}
		if status == http.StatusOK {
			var tok struct {
				AccessToken string `json:"access_token"`
			}
			if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
				return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Meta's login sent no access token", Scope: llm.ScopeEndpoint}
			}
			g, err := mint(ctx, set, tok.AccessToken)
			if err != nil {
				return nil, hideSecrets(err, dc.DeviceCode)
			}
			return g, nil
		}
		if status >= 500 {
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch e.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, refused("the sign-in was denied")
		case "expired_token":
			return nil, refused("the code expired before it was approved; start the login again")
		case "":
		default:
			return nil, hideSecrets(loginFailure(status, body, h), dc.DeviceCode)
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
	g, err := mint(ctx, set, c.RefreshToken)
	if err != nil {
		return nil, err
	}
	var old, minted credentialData
	_ = json.Unmarshal(c.Data, &old)
	_ = json.Unmarshal(g.Credential.Data, &minted)
	if minted.BaseURL == "" && old.BaseURL != "" {
		g.Credential.Data, _ = json.Marshal(old)
	}
	return g.Credential, nil
}

func mint(ctx context.Context, set settings, dca string) (*protocol.Grant, error) {
	b, _ := json.Marshal(map[string]string{"dca_token": dca})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, set.MintURL, bytes.NewReader(b))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad mint_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+dca)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)
	status, body, h, err := do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if status < 200 || status > 299 {
		return nil, hideSecrets(loginFailure(status, body, h), dca)
	}
	var minted struct {
		APIKey    string `json:"api_key"`
		BaseURL   string `json:"base_url"`
		UserEmail string `json:"user_email"`
	}
	if json.Unmarshal(body, &minted) != nil || strings.TrimSpace(minted.APIKey) == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Meta's key endpoint sent no API key", Scope: llm.ScopeEndpoint}
	}
	data, _ := json.Marshal(credentialData{BaseURL: strings.TrimSpace(minted.BaseURL)})
	return &protocol.Grant{
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: strings.TrimSpace(minted.APIKey), RefreshToken: dca, Data: data},
		Label:      minted.UserEmail,
		Identity:   identityOf(minted.UserEmail),
	}, nil
}

func identityOf(email string) map[string]string {
	if email = strings.TrimSpace(email); email != "" {
		return map[string]string{"email": email}
	}
	return nil
}

func postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)
	return do(req)
}

type sentError struct{ error }

func (e sentError) Unwrap() error { return e.error }

func wasSent(err error) bool {
	var s sentError
	return errors.As(err, &s)
}

func do(req *http.Request) (int, []byte, http.Header, error) {
	req, sent := sdk.TrackSend(req)
	resp, err := authClient.Do(req)
	if err != nil {
		if sent() {
			err = sentError{err}
		}
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, sentError{err}
	}
	return resp.StatusCode, body, resp.Header, nil
}

func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach Meta: " + err.Error(), Retryable: !wasSent(err), Scope: llm.ScopeEndpoint}
}

func loginFailure(status int, body []byte, h http.Header) *llm.Error {
	var b struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	_ = json.Unmarshal(body, &b)
	var code string
	if json.Unmarshal(b.Error, &code) != nil {
		var o struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		_ = json.Unmarshal(b.Error, &o)
		code, b.Description = o.Type, o.Message
	}
	code = sdk.LoginCode(code)
	msg := fmt.Sprintf("Meta answered %d", status)
	if code != "" {
		msg += " (" + code + ")"
	}
	if d := sdk.LoginText(b.Description); d != "" {
		msg += ": " + d
	}
	switch {
	case code == "invalid_grant" || code == "invalid_client" || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &llm.Error{Code: llm.CodeAuth, Message: msg + "; log in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: msg, Status: status, ProviderCode: code, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: sdk.RetryAfter(h.Get("Retry-After"), time.Now())}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: msg, Status: status, Scope: llm.ScopeEndpoint, RetryAfterMS: sdk.RetryAfter(h.Get("Retry-After"), time.Now())}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Status: status, ProviderCode: code, Scope: llm.ScopeEndpoint}
}

func hideSecrets(err error, secrets ...string) error {
	var e *llm.Error
	if !errors.As(err, &e) {
		return err
	}
	c := *e
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
