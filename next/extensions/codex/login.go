package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	clientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	redirectURI         = "http://localhost:1455/auth/callback"
	callbackAddr        = "127.0.0.1:1455"
	callbackPath        = "/auth/callback"
	loginScope          = "openid email profile offline_access"
	refreshScope        = "openid profile email"
	deviceRedirect      = "https://auth.openai.com/deviceauth/callback"
	deviceVerifyPath    = "/codex/device"
	defaultPollInterval = 5 * time.Second
	refreshLead         = 96 * time.Hour
)

var loginMethods = []protocol.LoginMethod{
	{Method: "browser", Kind: protocol.LoginBrowser, Label: "ChatGPT (browser)"},
	{Method: "device", Kind: protocol.LoginDevice, Label: "ChatGPT (device code)"},
	{Method: "siwc", Kind: protocol.LoginBrowser, Label: "Sign in with ChatGPT (SIWC)"},
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "OpenAI API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "API key", Secret: true},
		{Name: "base_url", Label: "Base URL, for a compatible provider including its /v1 (default OpenAI's)", Optional: true},
	}},
}

var authClient = &http.Client{
	Transport:     sdk.Upstream(http.DefaultTransport.(*http.Transport).Clone()),
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "browser":
		return browserLogin(ctx, set, s)
	case "device":
		return deviceLogin(ctx, set, s)
	case "siwc":
		return siwcLogin(ctx, set, s)
	case "api_key":
		return apiKeyLogin(ctx, set, call.Input)
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
}

var loginClient = &http.Client{
	Transport:     sdk.Upstream(upstreamTransport()),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const openAIHost = "api.openai.com"

func apiKeyBase(set settings, input string) (base string, chosen bool, err error) {
	raw := strings.TrimSpace(set.BaseURL)
	if raw == "" {
		raw = strings.TrimSpace(input)
	}
	if raw == "" {
		return strings.TrimRight(defaultAPIBaseURL, "/"), false, nil
	}
	raw = strings.TrimRight(raw, "/")
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", true, &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest,
			Message: "base_url must be an HTTP or HTTPS URL without user information, a query or a fragment"}
	}
	return raw, !strings.EqualFold(u.Hostname(), openAIHost), nil
}

func keyRefusal(e *llm.Error) bool {
	if e.Code == llm.CodeAuth || e.Code == llm.CodePermission || e.Code == llm.CodeAccountClosed {
		return true
	}
	switch strings.ToLower(e.ProviderCode) {
	case "invalid_api_key", "incorrect_api_key", "account_deactivated":
		return true
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "incorrect api key") || strings.Contains(m, "invalid api key")
}

func apiKeyLogin(ctx context.Context, set settings, input map[string]string) (*protocol.Grant, error) {
	key := strings.TrimSpace(input["api_key"])
	if key == "" {
		return nil, refused("enter an API key")
	}
	if strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the API key has a line break or another control character in it; paste the key alone", Scope: llm.ScopeRequest}
	}
	base, chosen, err := apiKeyBase(set, input["base_url"])
	if err != nil {
		return nil, err
	}
	who := "OpenAI"
	if chosen {
		u, _ := url.Parse(base)
		who = "the provider at " + u.Host
	}
	grant := &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}
	if b := strings.TrimSpace(input["base_url"]); b != "" && strings.TrimSpace(set.BaseURL) == "" {
		grant.Settings, _ = json.Marshal(map[string]string{"base_url": base})
	}
	redact := protocol.NewRedactor(grant.Credential)
	clean := func(e *llm.Error) error {
		e = redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
		e.Message = sdk.UpstreamText(e.Message, key)
		e.ProviderCode = sdk.LoginCode(e.ProviderCode)
		e.Raw = nil
		e.Retryable = false
		return e
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, authClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url", Scope: llm.ScopeRequest}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req, sent := sdk.TrackSend(req)
	resp, err := loginClient.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		e := &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach " + who + ": " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint}
		e = redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
		e.Message = sdk.UpstreamText(e.Message, key)
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, modelsMaxBody))
		var list struct {
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &list) == nil && list.Data != nil {
			return grant, nil
		}
		return nil, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Status: resp.StatusCode,
			Message: who + " did not answer with a model list, so the key could not be checked"}
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Status: resp.StatusCode,
			Message: who + " did not answer with a model list, so the key could not be checked"}
	}
	e := statusError(resp, "")
	if keyRefusal(e) {
		return nil, refused(who + " refused the key")
	}
	if (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) && chosen {
		return grant, nil
	}
	return nil, clean(e)
}

func browserLogin(ctx context.Context, set settings, s *sdk.LoginSession) (*protocol.Grant, error) {
	verifier := randomString(64)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(32)
	q := url.Values{
		"client_id":                  {clientID},
		"response_type":              {"code"},
		"redirect_uri":               {redirectURI},
		"scope":                      {loginScope},
		"state":                      {state},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method":      {"S256"},
		"prompt":                     {"login"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}
	authURL := strings.TrimRight(set.authBase(), "/") + "/oauth/authorize?" + q.Encode()
	params, err := awaitRedirect(ctx, set, s, authURL, state,
		"Open the link and sign in to ChatGPT. If the page you end on does not load, paste its address here.")
	if err != nil {
		return nil, err
	}
	tr, err := exchangeCode(ctx, set, params.Get("code"), verifier, redirectURI)
	if err != nil {
		return nil, err
	}
	return grantFrom(tr, nil)
}

func awaitRedirect(ctx context.Context, set settings, s *sdk.LoginSession, authURL, state, message string) (url.Values, error) {
	results := make(chan url.Values, 2)
	if set.CallbackListen == nil || *set.CallbackListen {
		if stop := listenCallback(callbackHandler(state, results)); stop != nil {
			defer stop()
		}
	}
	if err := s.Prompt(protocol.Prompt{Kind: protocol.LoginBrowser, URL: authURL, ExpiresIn: 900, Message: message,
		Inputs: []protocol.LoginInput{{Name: "redirect_url", Label: "Address the browser ended on"}}}); err != nil {
		return nil, err
	}
	fwdCtx, stopInputs := context.WithCancel(ctx)
	defer stopInputs()
	inputs, inputErr := forwardInputs(fwdCtx, s.Input)
	for {
		var params url.Values
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-inputErr:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		case params = <-results:
		case in, ok := <-inputs:
			if !ok {
				inputs = nil
				continue
			}
			if u, err := url.Parse(strings.TrimSpace(in["redirect_url"])); err == nil {
				params = u.Query()
			}
		}
		_, ok, err := signInResult(params, state)
		if err != nil {
			return nil, err
		}
		if ok {
			return params, nil
		}
		if err := s.Prompt(protocol.Prompt{Kind: protocol.LoginBrowser, URL: authURL,
			Message: "That address is not the result of this sign-in; open the link, sign in, and paste the whole address the browser ended on.",
			Inputs:  []protocol.LoginInput{{Name: "redirect_url", Label: "Address the browser ended on"}}}); err != nil {
			return nil, err
		}
	}
}

func forwardInputs(ctx context.Context, input func(context.Context) (map[string]string, error)) (<-chan map[string]string, <-chan error) {
	inputs := make(chan map[string]string, 1)
	inputErr := make(chan error, 1)
	go func() {
		defer close(inputs)
		for {
			in, err := input(ctx)
			if err != nil {
				inputErr <- err
				return
			}
			select {
			case inputs <- in:
			case <-ctx.Done():
				return
			}
		}
	}()
	return inputs, inputErr
}

func signInResult(params url.Values, state string) (code string, ok bool, err error) {
	if params.Get("state") != state {
		return "", false, nil
	}
	if e := params.Get("error"); e != "" {
		return "", true, refused("sign-in was refused: " + sdk.LoginText(e))
	}
	if code = params.Get("code"); code == "" {
		return "", true, refused("the sign-in result has no authorization code")
	}
	return code, true, nil
}

func callbackHandler(state string, results chan<- url.Values) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if q.Get("state") != state {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, "This sign-in result is not for the login pluxy is waiting for.")
			return
		}
		select {
		case results <- q:
		default:
		}
		if q.Get("error") != "" {
			fmt.Fprintln(w, "Sign-in was refused. You can close this window and go back to pluxy.")
			return
		}
		fmt.Fprintln(w, "Signed in. You can close this window and go back to pluxy.")
	}
}

func listenCallback(h http.HandlerFunc) (stop func()) {
	l, err := net.Listen("tcp", callbackAddr)
	if err != nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+callbackPath, h)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return func() { _ = srv.Close() }
}

func deviceLogin(ctx context.Context, set settings, s *sdk.LoginSession) (*protocol.Grant, error) {
	base := strings.TrimRight(set.authBase(), "/")
	var uc struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}
	status, body, err := postJSON(ctx, base+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": clientID})
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if status == http.StatusNotFound {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "device code sign-in is not available for this account; sign in with the browser", Status: status, Scope: llm.ScopeRequest}
	}
	if status < 200 || status > 299 {
		return nil, loginFailure(status, body)
	}
	if json.Unmarshal(body, &uc) != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the login server sent a malformed device code reply", Scope: llm.ScopeEndpoint}
	}
	if uc.UserCode == "" {
		uc.UserCode = uc.UserCodeAlt
	}
	if uc.DeviceAuthID == "" || uc.UserCode == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the login server sent no device code", Scope: llm.ScopeEndpoint}
	}
	interval := pollInterval(uc.Interval)
	if err := s.Prompt(protocol.Prompt{Kind: protocol.LoginDevice, UserCode: uc.UserCode, VerificationURI: base + deviceVerifyPath, ExpiresIn: 900,
		Message: "Open the link, sign in to ChatGPT and enter the code."}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		status, body, err := postJSON(ctx, base+"/api/accounts/deviceauth/token", map[string]string{"device_auth_id": uc.DeviceAuthID, "user_code": uc.UserCode})
		if err != nil {
			return nil, requestError(ctx, err)
		}
		switch {
		case status == http.StatusForbidden || status == http.StatusNotFound:
			continue
		case status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			if e := loginFailure(status, body); e.Code == llm.CodeAuth {
				return nil, e
			}
			return nil, refused(fmt.Sprintf("the device sign-in was refused or its code expired (%d)", status))
		case status < 200 || status > 299:
			return nil, loginFailure(status, body)
		}
		var tok struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if json.Unmarshal(body, &tok) != nil || tok.AuthorizationCode == "" || tok.CodeVerifier == "" {
			return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the login server sent an incomplete device sign-in result", Scope: llm.ScopeEndpoint}
		}
		tr, err := exchangeCode(ctx, set, tok.AuthorizationCode, tok.CodeVerifier, deviceRedirect)
		if err != nil {
			return nil, err
		}
		return grantFrom(tr, nil)
	}
}

func pollInterval(raw json.RawMessage) time.Duration {
	var n int64
	var s string
	switch {
	case json.Unmarshal(raw, &n) == nil:
	case json.Unmarshal(raw, &s) == nil:
		n, _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	}
	if n <= 0 || n > 60 {
		return defaultPollInterval
	}
	return time.Duration(n) * time.Second
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func exchangeCode(ctx context.Context, set settings, code, verifier, redirect string) (*tokenResponse, error) {
	return tokenRequest(ctx, set, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
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
	var d credentialData
	if len(c.Data) > 0 {
		_ = json.Unmarshal(c.Data, &d)
	}
	if d.SIWC {
		return siwcRefresh(ctx, set, c, d)
	}
	tr, err := tokenRequest(ctx, set, "/oauth/token", url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"scope":         {refreshScope},
	})
	if err != nil {
		return nil, err
	}
	g, err := grantFrom(tr, c)
	if err != nil {
		return nil, err
	}
	return g.Credential, nil
}

func tokenRequest(ctx context.Context, set settings, path string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(set.authBase(), "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad auth_base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	status, body, err := do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if status != http.StatusOK {
		return nil, loginFailure(status, body)
	}
	var tr tokenResponse
	if json.Unmarshal(body, &tr) != nil || tr.AccessToken == "" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the token endpoint sent no access token", Scope: llm.ScopeEndpoint}
	}
	return &tr, nil
}

func grantFrom(tr *tokenResponse, old *protocol.Credential) (*protocol.Grant, error) {
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	var d credentialData
	if old != nil {
		if c.RefreshToken == "" {
			c.RefreshToken = old.RefreshToken
		}
		if len(old.Data) > 0 {
			_ = json.Unmarshal(old.Data, &d)
		}
	}
	if tr.IDToken != "" {
		d.IDToken = tr.IDToken
	}
	claims, _ := parseIDToken(d.IDToken)
	if claims.accountID != "" {
		d.AccountID = claims.accountID
	}
	if d.AccountID == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the sign-in result names no ChatGPT account", Scope: llm.ScopeCredential}
	}
	c.Data = raw(d)
	now := time.Now()
	if exp := jwtExpiry(c.AccessToken); exp > 0 {
		c.ExpiresAt = min(exp, now.Add(sdk.MaxTokenLife).Unix())
	} else {
		c.ExpiresAt = sdk.ExpiryUnix(now, tr.ExpiresIn)
	}
	if c.ExpiresAt > 0 {
		if at := time.Unix(c.ExpiresAt, 0).Add(-refreshLead); at.After(now) {
			c.RefreshAt = at.Unix()
		}
	}
	identity := map[string]string{}
	if claims.email != "" {
		identity["email"] = claims.email
	}
	if d.AccountID != "" {
		identity["account_id"] = d.AccountID
	}
	return &protocol.Grant{Credential: c, Label: claims.email, Identity: identity}, nil
}

type idClaims struct {
	email, accountID, planType string
}

func parseIDToken(tok string) (idClaims, bool) {
	var c struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			PlanType  string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if !jwtClaims(tok, &c) {
		return idClaims{}, false
	}
	return idClaims{email: c.Email, accountID: c.Auth.AccountID, planType: c.Auth.PlanType}, true
}

func jwtExpiry(tok string) int64 {
	var c struct {
		Exp int64 `json:"exp"`
	}
	if !jwtClaims(tok, &c) {
		return 0
	}
	return c.Exp
}

func jwtClaims(tok string, v any) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func postJSON(ctx context.Context, endpoint string, v any) (int, []byte, error) {
	b, _ := json.Marshal(v)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return do(req)
}

type sentError struct{ error }

func (e sentError) Unwrap() error { return e.error }

func do(req *http.Request) (int, []byte, error) {
	req, sent := sdk.TrackSend(req)
	resp, err := authClient.Do(req)
	if err != nil {
		if sent() {
			err = sentError{err}
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, sentError{err}
	}
	return resp.StatusCode, body, nil
}

func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var sent sentError
	return &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the login server: " + err.Error(), Retryable: !errors.As(err, &sent), Scope: llm.ScopeEndpoint}
}

var grantRefusals = []string{"invalid_grant", "refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated", "token_revoked", "invalid_client"}

func loginFailure(status int, body []byte) *llm.Error {
	var b struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	var code string
	if json.Unmarshal(b.Error, &code) != nil {
		var o struct {
			Code string `json:"code"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(b.Error, &o)
		code = o.Code
		if code == "" {
			code = o.Type
		}
	}
	code = sdk.LoginCode(code)
	for _, r := range grantRefusals {
		if code == r {
			return &llm.Error{Code: llm.CodeAuth, Message: "OpenAI refused the sign-in (" + code + "); sign in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
		}
	}
	switch {
	case status == http.StatusUnauthorized:
		return &llm.Error{Code: llm.CodeAuth, Message: "OpenAI refused the sign-in; sign in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "the login server is rate limiting", Status: status, ProviderCode: code, Retryable: true, Scope: llm.ScopeCredential}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("the login server answered %d", status), Status: status, Scope: llm.ScopeEndpoint}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the login server answered %d (%s)", status, code), Status: status, ProviderCode: code, Scope: llm.ScopeEndpoint}
}

func refused(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
