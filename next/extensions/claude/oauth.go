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
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	defaultAuthorizeURL = "https://claude.ai/oauth/authorize"
	defaultTokenURL     = "https://platform.claude.com/v1/oauth/token"
	defaultClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

const redirectURI = "http://localhost:54545/callback"

const scopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

const renewAhead = time.Hour

var loginMethods = []protocol.LoginMethod{
	{Method: "subscription", Kind: protocol.LoginBrowser, Label: "Claude subscription"},
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "Anthropic API key", Secret: true},
	}},
	{Method: "setup_token", Kind: protocol.LoginAPIKey, Label: "Setup token", Inputs: []protocol.LoginInput{
		{Name: "setup_token", Label: "Token from claude setup-token", Secret: true},
	}},
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "subscription":
		return subscriptionLogin(ctx, set, s)
	case "api_key":
		return apiKeyLogin(ctx, set, call.Input["api_key"])
	case "setup_token":
		return setupTokenLogin(call.Input["setup_token"])
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
}

func subscriptionLogin(ctx context.Context, set settings, s *sdk.LoginSession) (*protocol.Grant, error) {
	verifier := randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(32)
	authURL := set.AuthorizeURL + "?" + url.Values{
		"code":                  {"true"},
		"client_id":             {set.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}.Encode()
	err := s.Prompt(protocol.Prompt{
		Kind:    protocol.PromptBrowser,
		Message: "Open the URL and sign in to Claude. Then paste the address the browser was sent to (it starts with " + redirectURI + "), or the code the page shows.",
		URL:     authURL,
		Inputs:  []protocol.LoginInput{{Name: "redirect_url", Label: "Redirect address or code"}},
	})
	if err != nil {
		return nil, err
	}
	in, err := s.Input(ctx)
	if err != nil {
		return nil, err
	}
	code, gotState, failure := readCallback(in["redirect_url"])
	switch {
	case failure != "":
		return nil, callbackRefusal(failure)
	case code == "":
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the pasted text has no code", Scope: llm.ScopeRequest}
	case gotState != state:
		return nil, refused("the pasted code belongs to another sign-in")
	}
	var tr tokenResponse
	if f := tokenPost(ctx, set.TokenURL, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  redirectURI,
		"client_id":     set.ClientID,
		"code_verifier": verifier,
		"state":         state,
	}, &tr); f != nil {
		return nil, requestError(ctx, f)
	}
	if tr.AccessToken == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the token endpoint sent no access token", Scope: llm.ScopeEndpoint}
	}
	return &protocol.Grant{Credential: tr.credential(time.Now()), Label: tr.Account.EmailAddress, Identity: tr.identity()}, nil
}

func readCallback(pasted string) (code, state, failure string) {
	pasted = strings.TrimSpace(pasted)
	if u, err := url.Parse(pasted); err == nil && u.Scheme != "" && u.Host != "" {
		q := u.Query()
		if e := q.Get("error"); e != "" {
			return "", "", e
		}
		code, state = q.Get("code"), q.Get("state")
		if c, s, ok := strings.Cut(code, "#"); ok && state == "" {
			code, state = c, s
		}
		return code, state, ""
	}
	code, state, _ = strings.Cut(pasted, "#")
	return code, state, ""
}

func setupTokenLogin(token string) (*protocol.Grant, error) {
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "enter the token claude setup-token printed", Scope: llm.ScopeRequest}
	case !isOAuthToken(token):
		return nil, refused("this is not a setup token (a setup token contains sk-ant-oat); add an API key with the api_key method")
	}
	return &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: token}}, nil
}

func apiKeyLogin(ctx context.Context, set settings, key string) (*protocol.Grant, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "enter an API key", Scope: llm.ScopeRequest}
	}
	if isOAuthToken(key) {
		return setupTokenLogin(key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/v1/models?limit=1", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Anthropic-Version", anthropicVersion)
	req, sent := sdk.TrackSend(req)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach Anthropic: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, refused("Anthropic refused the key")
	default:
		e := upstreamError(resp, "", false)
		if e.Code == llm.CodeAccountClosed {
			return nil, refused("Anthropic refused the key")
		}
		if e.Status >= 500 {
			e.Retryable = false
		}
		r := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
		return nil, r.Event(llm.Event{Type: llm.EventError, Error: e}).Error
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
	var tr tokenResponse
	if f := tokenPost(ctx, set.TokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.RefreshToken,
		"client_id":     set.ClientID,
	}, &tr); f != nil {
		return nil, requestError(ctx, f)
	}
	if tr.AccessToken == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the token endpoint sent no access token", Scope: llm.ScopeEndpoint}
	}
	out := tr.credential(time.Now())
	if out.RefreshToken == "" {
		out.RefreshToken = c.RefreshToken
	}
	return out, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
	} `json:"organization"`
}

func (tr *tokenResponse) identity() map[string]string {
	id := map[string]string{}
	for k, v := range map[string]string{"email": tr.Account.EmailAddress, "account_id": tr.Account.UUID, "organization_id": tr.Organization.UUID} {
		if v != "" {
			id[k] = v
		}
	}
	return id
}

func (tr *tokenResponse) credential(now time.Time) *protocol.Credential {
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if exp := sdk.ExpiryUnix(now, tr.ExpiresIn); exp > 0 {
		c.ExpiresAt = exp
		ahead := min(int64(renewAhead/time.Second), (exp-now.Unix())/2)
		c.RefreshAt = c.ExpiresAt - ahead
	}
	return c
}

type tokenFailure struct {
	status       int
	code         string
	retryAfterMS int64
	err          error
	sent         bool
}

func tokenPost(ctx context.Context, endpoint string, fields map[string]string, out any) *tokenFailure {
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &tokenFailure{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req, sent := sdk.TrackSend(req)
	resp, err := client.Do(req)
	if err != nil {
		return &tokenFailure{err: err, sent: sent()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error any `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		f := &tokenFailure{status: resp.StatusCode, retryAfterMS: sdk.RetryAfter(resp.Header.Get("Retry-After"), time.Now()), sent: true}
		switch v := e.Error.(type) {
		case string:
			f.code = v
		case map[string]any:
			f.code, _ = v["type"].(string)
		}
		f.code = sdk.LoginCode(f.code)
		return f
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return &tokenFailure{status: resp.StatusCode, err: fmt.Errorf("the reply is not valid JSON: %w", err), sent: true}
	}
	return nil
}

func requestError(ctx context.Context, f *tokenFailure) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return tokenError(f)
}

var terminalCodes = map[string]bool{
	"invalid_grant":             true,
	"invalid_client":            true,
	"unauthorized_client":       true,
	"refresh_token_reused":      true,
	"refresh_token_expired":     true,
	"refresh_token_invalidated": true,
	"token_revoked":             true,
}

func tokenError(f *tokenFailure) error {
	switch {
	case f.status == 0:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("could not reach the token endpoint: %v", f.err), Retryable: !f.sent, Scope: llm.ScopeEndpoint}
	case f.err != nil:
		return &llm.Error{Code: llm.CodeUpstream, Message: f.err.Error(), Status: f.status, Scope: llm.ScopeEndpoint}
	case terminalCodes[f.code] || f.status == http.StatusUnauthorized:
		return &llm.Error{Code: llm.CodeAuth, Message: "Claude refused the grant; log in again", Status: f.status, ProviderCode: f.code, Scope: llm.ScopeCredential}
	case f.status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "the token endpoint is rate limiting", Status: f.status, ProviderCode: f.code, Retryable: true, Scope: llm.ScopeEndpoint, RetryAfterMS: f.retryAfterMS}
	case f.status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("the token endpoint answered %d", f.status), Status: f.status, Scope: llm.ScopeEndpoint, RetryAfterMS: f.retryAfterMS}
	default:
		return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the token endpoint answered %d (%s)", f.status, f.code), Status: f.status, ProviderCode: f.code, Scope: llm.ScopeEndpoint}
	}
}

func callbackRefusal(failure string) error {
	return refused("Claude refused the sign-in: " + sdk.LoginText(failure))
}

func refused(msg string) error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
