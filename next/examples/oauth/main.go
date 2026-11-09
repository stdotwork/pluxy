// Command oauth is an example provider extension that signs in with OAuth. It has three login
// methods: the device authorization grant (RFC 8628), the authorization code grant with PKCE (RFC
// 6749 and RFC 7636), and an API key. It renews OAuth credentials with their refresh token, and it
// calls a model API with the credential's access token or key. It checks an account by asking the
// provider whether its token works.
//
// Account settings:
//
//	{"base_url": "https://provider.example"}
//
// The provider is any server that speaks the endpoints of the oauthtest package: /oauth/device/code,
// /oauth/authorize, /oauth/token, /v1/chat and /v1/me.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// clientID is the OAuth client this extension identifies as.
const clientID = "example-client"

// redirectURI is where the browser login sends the user after sign-in. Nothing listens there: the
// user pastes the address they were sent to back into pluxy.
const redirectURI = "http://localhost:1455/callback"

// deviceGrant is the grant type of the device authorization grant (RFC 8628 section 3.4).
const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// defaultPollInterval is how often a device login polls when the provider does not say (RFC 8628
// section 3.2).
const defaultPollInterval = 5 * time.Second

// slowDownStep is how much a device login adds to its interval after slow_down (RFC 8628 section
// 3.5).
const slowDownStep = 5 * time.Second

// loginMethods are the ways to sign in.
var loginMethods = []protocol.LoginMethod{
	{Method: "device", Kind: protocol.LoginDevice, Label: "Device code"},
	{Method: "browser", Kind: protocol.LoginBrowser, Label: "Browser"},
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "API key", Secret: true},
	}},
}

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           "example/oauth",
		Version:      "0.1.0",
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		Check:        check,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "oauth:", err)
		os.Exit(1)
	}
}

// baseURL reads base_url from the account settings.
func baseURL(settings json.RawMessage) (string, error) {
	var s struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(settings, &s); err != nil || s.BaseURL == "" {
		return "", &llm.Error{Code: llm.CodeInvalidRequest, Message: `the account settings need "base_url"`, Scope: llm.ScopeCredential}
	}
	return strings.TrimSuffix(s.BaseURL, "/"), nil
}

// bearerOf returns the token a generate call sends: the access token of an OAuth credential, or
// the key of an API key credential.
func bearerOf(c *protocol.Credential) (string, error) {
	if c != nil {
		switch {
		case c.Kind == protocol.CredentialOAuth && c.AccessToken != "":
			return c.AccessToken, nil
		case c.Kind == protocol.CredentialAPIKey && c.APIKey != "":
			return c.APIKey, nil
		}
	}
	return "", &llm.Error{Code: llm.CodeAuth, Message: "the account has no usable credential; log in again", Scope: llm.ScopeCredential}
}

// generate streams a model reply from the provider's /v1/chat, passing each delta on as it arrives.
func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	base, err := baseURL(call.Account.Settings)
	if err != nil {
		return err
	}
	token, err := bearerOf(call.Account.Credential)
	if err != nil {
		return err
	}
	resp, err := chat(ctx, base, token, call.Request.Model, call.Request.Stream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return chatError(resp)
	}
	if err := out.Start(llm.ResponseInfo{ID: "oauth-" + call.ID, Model: call.Request.Model, Created: time.Now().Unix()}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var line chatLine
		if err := dec.Decode(&line); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream reply stopped early: %v", err)}
		}
		switch {
		case line.Done:
			if err := out.EndItem(item); err != nil {
				return err
			}
			return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: "stop"},
				&llm.Usage{InputTokens: line.InputTokens, OutputTokens: line.OutputTokens})
		case line.Delta != "":
			if err := out.Text(item, line.Delta); err != nil {
				return err
			}
		}
	}
}

// chatLine is one line of the /v1/chat reply: a text delta, or the end with its usage.
type chatLine struct {
	Delta        string `json:"delta"`
	Done         bool   `json:"done"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

// chat sends a chat request with the bearer token. A cancelled ctx stops the request.
func chat(ctx context.Context, base, token, model string, stream bool) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"model": model, "stream": stream})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the base_url is not a valid URL", Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the provider", Retryable: true, Scope: llm.ScopeEndpoint}
	}
	return resp, nil
}

// check asks the provider's /v1/me whether the account's token works: a call that costs the
// account nothing and generates nothing.
func check(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
	base, err := baseURL(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	token, err := bearerOf(call.Account.Credential)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/me", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the base_url is not a valid URL", Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the provider", Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, chatError(resp)
	}
	return &protocol.CheckResult{Detail: "the provider accepted the token"}, nil
}

// chatError turns a refused /v1/chat request into an error.
func chatError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &llm.Error{Code: llm.CodeAuth, Message: "the provider refused the credential", Status: resp.StatusCode, Retryable: true, Scope: llm.ScopeCredential}
	case http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "the provider is rate limiting this credential", Status: resp.StatusCode, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: retryAfterMS(resp)}
	default:
		return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the provider answered %d", resp.StatusCode), Status: resp.StatusCode}
	}
}

// retryAfterMS reads a Retry-After header given in seconds, in milliseconds; 0 when absent.
func retryAfterMS(resp *http.Response) int64 {
	secs, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64)
	if err != nil || secs < 0 {
		return 0
	}
	return secs * 1000
}

// login signs in with the method the call names.
func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	base, err := baseURL(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "device":
		return deviceLogin(ctx, base, s)
	case "browser":
		return browserLogin(ctx, base, s)
	case "api_key":
		return apiKeyLogin(ctx, base, call.Input)
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
}

// deviceLogin shows the user a code to enter on another device and polls until it is approved,
// refused or expired (RFC 8628 section 3.4 and 3.5).
func deviceLogin(ctx context.Context, base string, s *sdk.LoginSession) (*protocol.Grant, error) {
	var dc struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if f := oauthPost(ctx, base+"/oauth/device/code", url.Values{"client_id": {clientID}}, &dc); f != nil {
		return nil, requestError(ctx, f)
	}
	err := s.Prompt(protocol.Prompt{
		Kind:                    protocol.PromptDevice,
		Message:                 "Open the verification page on another device and enter the code.",
		UserCode:                dc.UserCode,
		VerificationURI:         dc.VerificationURI,
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               dc.ExpiresIn,
	})
	if err != nil {
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
		f := oauthPost(ctx, base+"/oauth/token", url.Values{
			"grant_type":  {deviceGrant},
			"device_code": {dc.DeviceCode},
			"client_id":   {clientID},
		}, &tr)
		if f == nil {
			return grantFrom(&tr), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch f.code {
		case "authorization_pending":
		case "slow_down":
			interval += slowDownStep
		case "access_denied":
			return nil, &llm.Error{Code: llm.CodeAuth, Message: "the sign-in was denied", Scope: llm.ScopeCredential}
		case "expired_token":
			return nil, &llm.Error{Code: llm.CodeAuth, Message: "the code expired before it was approved; start the login again", Scope: llm.ScopeCredential}
		default:
			return nil, tokenError(f)
		}
	}
}

// browserLogin sends the user to the provider's sign-in page with PKCE (RFC 7636), then takes the
// address the browser was sent to, which the user pastes back, and exchanges its code.
func browserLogin(ctx context.Context, base string, s *sdk.LoginSession) (*protocol.Grant, error) {
	verifier := randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(16)
	authURL := base + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	err := s.Prompt(protocol.Prompt{
		Kind:    protocol.PromptBrowser,
		Message: "Open the URL and sign in. Then paste the address the browser was sent to, which starts with " + redirectURI + ".",
		URL:     authURL,
		Inputs:  []protocol.LoginInput{{Name: "redirect_url", Label: "Redirect URL"}},
	})
	if err != nil {
		return nil, err
	}
	in, err := s.Input(ctx)
	if err != nil {
		return nil, err
	}
	redirect, err := url.Parse(in["redirect_url"])
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the pasted text is not an address", Scope: llm.ScopeRequest}
	}
	q := redirect.Query()
	if q.Get("state") != state {
		return nil, refused("the pasted address belongs to another sign-in")
	}
	if e := q.Get("error"); e != "" {
		return nil, refused("the provider refused the sign-in: " + e)
	}
	code := q.Get("code")
	if code == "" {
		return nil, refused("the pasted address has no code")
	}
	var tr tokenResponse
	if f := oauthPost(ctx, base+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}, &tr); f != nil {
		return nil, requestError(ctx, f)
	}
	return grantFrom(&tr), nil
}

// apiKeyLogin checks a key with one /v1/chat request and keeps it when the provider accepts it.
func apiKeyLogin(ctx context.Context, base string, input map[string]string) (*protocol.Grant, error) {
	key := input["api_key"]
	if key == "" {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "enter an API key", Scope: llm.ScopeRequest}
	}
	resp, err := chat(ctx, base, key, "key-check", false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}, nil
	case http.StatusUnauthorized:
		return nil, refused("the key was refused")
	default:
		return nil, chatError(resp)
	}
}

// refresh renews an OAuth credential with its refresh token (RFC 6749 section 6).
func refresh(ctx context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
	base, err := baseURL(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c == nil || c.Kind != protocol.CredentialOAuth || c.RefreshToken == "" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "this credential cannot be refreshed", Scope: llm.ScopeRequest}
	}
	var tr tokenResponse
	if f := oauthPost(ctx, base+"/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {c.RefreshToken},
	}, &tr); f != nil {
		return nil, requestError(ctx, f)
	}
	// The provider may not rotate the refresh token; then the old one still works.
	g := grantFrom(&tr).Credential
	if g.RefreshToken == "" {
		g.RefreshToken = c.RefreshToken
	}
	return g, nil
}

// tokenResponse is a successful reply of the token endpoint (RFC 6749 section 5.1).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Email        string `json:"email"`
}

// grantFrom builds the login's grant from a token reply.
func grantFrom(tr *tokenResponse) *protocol.Grant {
	return &protocol.Grant{
		Credential: &protocol.Credential{
			Kind:         protocol.CredentialOAuth,
			AccessToken:  tr.AccessToken,
			RefreshToken: tr.RefreshToken,
			ExpiresAt:    expiresAt(tr.ExpiresIn),
		},
		Label: tr.Email,
	}
}

// expiresAt returns when a token that lasts expiresIn seconds stops working; 0 when unknown.
func expiresAt(expiresIn int64) int64 {
	if expiresIn <= 0 {
		return 0
	}
	return time.Now().Unix() + expiresIn
}

// oauthFailure is a reply that is not a success: a refusal (status and an RFC 6749 error code), or
// a failure to get a reply (status 0 and err set).
type oauthFailure struct {
	status       int
	code         string
	retryAfterMS int64
	err          error
}

// oauthPost posts form to endpoint and decodes a 200 reply into out. Any other outcome is returned
// as an oauthFailure.
func oauthPost(ctx context.Context, endpoint string, form url.Values, out any) *oauthFailure {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return &oauthFailure{err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &oauthFailure{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return &oauthFailure{status: resp.StatusCode, code: e.Error, retryAfterMS: retryAfterMS(resp)}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return &oauthFailure{status: resp.StatusCode, err: fmt.Errorf("the reply is not valid JSON: %w", err)}
	}
	return nil
}

// requestError is the error for a failed token or device request: the context's error when the
// call was cancelled, otherwise what the failure means.
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
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("could not reach the token endpoint: %v", f.err), Retryable: true, Scope: llm.ScopeEndpoint}
	case f.err != nil:
		return &llm.Error{Code: llm.CodeUpstream, Message: f.err.Error(), Status: f.status, Scope: llm.ScopeEndpoint}
	case f.status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "the token endpoint is rate limiting", Status: f.status, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: f.retryAfterMS}
	case f.status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("the token endpoint answered %d", f.status), Status: f.status, Retryable: true, Scope: llm.ScopeEndpoint}
	case f.code == "invalid_grant":
		return &llm.Error{Code: llm.CodeAuth, Message: "the provider refused the grant; log in again", Status: f.status, ProviderCode: f.code, Scope: llm.ScopeCredential}
	default:
		return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the token endpoint answered %d (%s)", f.status, f.code), Status: f.status, ProviderCode: f.code, Scope: llm.ScopeEndpoint}
	}
}

// refused is the error for a sign-in the user or the provider turned down.
func refused(msg string) error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

// randomString returns n random bytes, base64url encoded without padding.
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
