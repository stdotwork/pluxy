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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Antigravity signs in with Google's OAuth 2.0 for installed apps, as the Antigravity app does and
// as today's core speaks it: the browser signs in at Google with Antigravity's OAuth client and
// comes back to a loopback address, and the authorization code is traded at Google's token
// endpoint. Google issues no client registration for Antigravity to other programs, so the login
// uses the app's own client, as pluxy's Codex and Kimi extensions do with OpenAI's and Kimi's.
// Then the account's Google Cloud project is found, or made, with Cloud Code's loadCodeAssist and
// onboardUser calls.
//
// Google's token endpoint also takes the client's secret. pluxy does not ship it: the user enters
// it at login, and it is kept in the credential, which core keeps secret, for renewals.
const (
	clientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	callbackAddr = "127.0.0.1:51121"
	callbackPath = "/oauth-callback"
	// redirectURI is the loopback redirect Antigravity's client takes. The browser is sent there
	// after sign-in; the extension listens on it when it can, and the user can paste the address
	// otherwise.
	redirectURI = "http://localhost:51121" + callbackPath
	// refreshLead is how long before its access token expires an account is renewed, as today's
	// core renews it. Google's access tokens last an hour.
	refreshLead = 30 * time.Minute
	// onboardAttempts and onboardWait bound waiting for onboardUser to make a project, as
	// today's core waits.
	onboardAttempts = 5
	onboardWait     = 2 * time.Second
)

// Google's login endpoints.
const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
)

var scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/aicode",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

var loginMethods = []protocol.LoginMethod{
	{Method: "browser", Kind: protocol.LoginBrowser, Label: "Google account (browser)", Inputs: []protocol.LoginInput{
		{Name: "client_secret", Label: "Antigravity OAuth client secret", Secret: true},
	}},
}

// endpoints are Google's login endpoints, or a test server's (auth_base_url).
type endpoints struct{ auth, token, userinfo string }

func (s settings) endpoints() endpoints {
	if s.AuthBaseURL != "" {
		return endpoints{s.AuthBaseURL + "/o/oauth2/v2/auth", s.AuthBaseURL + "/token", s.AuthBaseURL + "/oauth2/v2/userinfo?alt=json"}
	}
	return endpoints{googleAuthURL, googleTokenURL, googleUserInfoURL}
}

// authClient talks to Google's login and Cloud Code's project calls. Its timeout covers only
// signing in and renewing, never a model request. It follows no redirect, which would send the
// form (a code or refresh token, and the client secret) wherever the redirect points.
var authClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if call.Method != "browser" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
	}
	secret := strings.TrimSpace(call.Input["client_secret"])
	if secret == "" {
		return nil, refused("enter the Antigravity OAuth client secret")
	}
	return browserLogin(ctx, set, secret, s)
}

// browserLogin runs the authorization code flow, with PKCE (RFC 7636) on top of what today's core
// sends. The browser comes back to redirectURI, which the extension serves on this machine when
// the port is free; otherwise the user pastes the address the browser ended on.
func browserLogin(ctx context.Context, set settings, secret string, s *sdk.LoginSession) (*protocol.Grant, error) {
	verifier := randomString(64)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(32)
	q := url.Values{
		"access_type":           {"offline"},
		"client_id":             {clientID},
		"prompt":                {"consent"},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	ep := set.endpoints()
	authURL := ep.auth + "?" + q.Encode()

	results := make(chan url.Values, 2)
	if set.CallbackListen == nil || *set.CallbackListen {
		if stop := listenCallback(callbackHandler(state, results)); stop != nil {
			defer stop()
		}
	}
	prompt := protocol.Prompt{Kind: protocol.LoginBrowser, URL: authURL, ExpiresIn: 900,
		Message: "Open the link and sign in with the Google account you use in Antigravity. If the page you end on does not load, paste its address here.",
		Inputs:  []protocol.LoginInput{{Name: "redirect_url", Label: "Address the browser ended on"}}}
	if err := s.Prompt(prompt); err != nil {
		return nil, err
	}
	inputs := make(chan map[string]string, 1)
	inputErr := make(chan error, 1)
	go func() {
		for {
			in, err := s.Input(ctx)
			if err != nil {
				inputErr <- err
				return
			}
			inputs <- in
		}
	}()
	var code string
	for code == "" {
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
		case in := <-inputs:
			if u, err := url.Parse(strings.TrimSpace(in["redirect_url"])); err == nil {
				params = u.Query()
			}
		}
		c, ok, err := signInResult(params, state)
		if err != nil {
			return nil, err
		}
		if !ok {
			prompt.Message = "That address is not the result of this sign-in; open the link, sign in, and paste the whole address the browser ended on."
			if err := s.Prompt(prompt); err != nil {
				return nil, err
			}
			continue
		}
		code = c
	}
	tr, err := tokenRequest(ctx, ep.token, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return nil, err
	}
	email, err := fetchEmail(ctx, ep.userinfo, tr.AccessToken, set.UserAgent)
	if err != nil {
		return nil, err
	}
	project, err := findProject(ctx, set, tr.AccessToken)
	if err != nil {
		return nil, err
	}
	return &protocol.Grant{Credential: tr.credential(credentialData{ProjectID: project, ClientSecret: secret}, ""), Label: email}, nil
}

// signInResult reads a browser sign-in result, the query of the address the browser was sent back
// to. ok is false when the result is not for this login (its state differs), and it is then
// ignored; a refusal or a result with no code ends the login.
func signInResult(params url.Values, state string) (code string, ok bool, err error) {
	if params.Get("state") != state {
		return "", false, nil
	}
	if e := params.Get("error"); e != "" {
		return "", true, refused("sign-in was refused: " + e)
	}
	if code = params.Get("code"); code == "" {
		return "", true, refused("the sign-in result has no authorization code")
	}
	return code, true, nil
}

// callbackHandler answers the browser's redirect and sends a sign-in result for this login
// (state) to results. A result for any other login is answered and not passed on.
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

// listenCallback serves h on the callback path at callbackAddr, on loopback only. It returns nil
// when the address is taken.
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

// refresh renews an account's access token with its refresh token, and finds the account's
// project when its credential has none.
func refresh(ctx context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c == nil || c.Kind != protocol.CredentialOAuth || c.RefreshToken == "" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "this credential cannot be renewed", Scope: llm.ScopeRequest}
	}
	data := readData(c)
	if data.ClientSecret == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the credential has no Antigravity OAuth client secret (client_secret in its data); log in again", Scope: llm.ScopeCredential}
	}
	tr, err := tokenRequest(ctx, set.endpoints().token, url.Values{
		"client_id":     {clientID},
		"client_secret": {data.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
	})
	if err != nil {
		return nil, err
	}
	if data.ProjectID == "" {
		if data.ProjectID, err = findProject(ctx, set, tr.AccessToken); err != nil {
			return nil, err
		}
	}
	return tr.credential(data, c.RefreshToken), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// credential builds a credential from a token reply. A reply without a refresh token keeps
// oldRefresh: Google sends one only at sign-in.
func (tr *tokenResponse) credential(data credentialData, oldRefresh string) *protocol.Credential {
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if c.RefreshToken == "" {
		c.RefreshToken = oldRefresh
	}
	if tr.ExpiresIn > 0 {
		now := time.Now()
		c.ExpiresAt = now.Unix() + tr.ExpiresIn
		if at := time.Unix(c.ExpiresAt, 0).Add(-refreshLead); at.After(now) {
			c.RefreshAt = at.Unix()
		}
	}
	c.Data, _ = json.Marshal(data)
	return c
}

func tokenRequest(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
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
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Google's token endpoint sent no access token", Scope: llm.ScopeEndpoint}
	}
	return &tr, nil
}

// fetchEmail returns the signed-in account's e-mail address, the account's label.
func fetchEmail(ctx context.Context, endpoint, token, ua string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad auth_base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", ua)
	status, body, err := do(req)
	if err != nil {
		return "", requestError(ctx, err)
	}
	if status != http.StatusOK {
		return "", loginFailure(status, body)
	}
	var info struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &info) != nil || strings.TrimSpace(info.Email) == "" {
		return "", &llm.Error{Code: llm.CodeUpstream, Message: "Google's user info names no e-mail address", Scope: llm.ScopeEndpoint}
	}
	return strings.TrimSpace(info.Email), nil
}

// findProject returns the Google Cloud project Cloud Code gives the account: the one
// loadCodeAssist names, or one onboardUser makes on the account's default tier.
func findProject(ctx context.Context, set settings, token string) (string, error) {
	var load struct {
		Project      json.RawMessage `json:"cloudaicompanionProject"`
		ProjectID    json.RawMessage `json:"projectId"`
		ProjectPlain json.RawMessage `json:"project"`
		AllowedTiers []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
		CurrentTier struct {
			ID string `json:"id"`
		} `json:"currentTier"`
	}
	h := http.Header{"User-Agent": {set.UserAgent}}
	status, body, err := cloudCode(ctx, set.BaseURL+"/v1internal:loadCodeAssist", token, h, map[string]any{"metadata": map[string]any{"ideType": "ANTIGRAVITY"}})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", projectFailure("loadCodeAssist", status, body)
	}
	if json.Unmarshal(body, &load) != nil {
		return "", &llm.Error{Code: llm.CodeUpstream, Message: "Cloud Code's loadCodeAssist reply is not valid JSON", Scope: llm.ScopeEndpoint}
	}
	for _, raw := range []json.RawMessage{load.Project, load.ProjectID, load.ProjectPlain} {
		if p := projectOf(raw); p != "" {
			return p, nil
		}
	}
	tier := "free-tier"
	if load.CurrentTier.ID != "" {
		tier = load.CurrentTier.ID
	}
	for _, t := range load.AllowedTiers {
		if t.IsDefault && t.ID != "" {
			tier = t.ID
			break
		}
	}
	h = http.Header{
		"User-Agent":        {set.UserAgent + " google-api-nodejs-client/10.3.0"},
		"X-Goog-Api-Client": {"gl-node/22.21.1"},
		"Accept":            {"*/*"},
	}
	onboard := map[string]any{"tier_id": tier, "metadata": map[string]any{"ide_type": "ANTIGRAVITY", "ide_version": appVersion, "ide_name": "antigravity"}}
	for attempt := 1; ; attempt++ {
		status, body, err := cloudCode(ctx, set.BaseURL+"/v1internal:onboardUser", token, h, onboard)
		if err != nil {
			return "", err
		}
		if status != http.StatusOK {
			return "", projectFailure("onboardUser", status, body)
		}
		var op struct {
			Done     bool `json:"done"`
			Response struct {
				Project   json.RawMessage `json:"cloudaicompanionProject"`
				ProjectID json.RawMessage `json:"projectId"`
				Plain     json.RawMessage `json:"project"`
			} `json:"response"`
		}
		_ = json.Unmarshal(body, &op)
		if op.Done {
			for _, raw := range []json.RawMessage{op.Response.Project, op.Response.ProjectID, op.Response.Plain} {
				if p := projectOf(raw); p != "" {
					return p, nil
				}
			}
			return "", &llm.Error{Code: llm.CodeUpstream, Message: "Cloud Code finished onboarding without naming a project", Scope: llm.ScopeEndpoint}
		}
		if attempt >= onboardAttempts {
			return "", &llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("Cloud Code did not finish onboarding the account after %d attempts; log in again later", onboardAttempts), Retryable: true, Scope: llm.ScopeEndpoint}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(onboardWait):
		}
	}
}

// projectOf reads a project from Cloud Code: its ID as a string, or an object with an id.
func projectOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var o struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return strings.TrimSpace(o.ID)
	}
	return ""
}

func cloudCode(ctx context.Context, endpoint, token string, h http.Header, v any) (int, []byte, error) {
	b, _ := json.Marshal(v)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	for k, vs := range h {
		req.Header[k] = vs
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	status, body, err := do(req)
	if err != nil {
		return 0, nil, requestError(ctx, err)
	}
	return status, body, nil
}

func projectFailure(call string, status int, body []byte) error {
	e := loginFailure(status, body)
	e.Message = fmt.Sprintf("Cloud Code's %s answered %d: %s", call, status, e.Message)
	return e
}

func do(req *http.Request) (int, []byte, error) {
	resp, err := authClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// requestError is the error for a login request that got no reply.
func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e, ok := err.(*llm.Error); ok {
		return e
	}
	return &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach Google: " + err.Error(), Retryable: true, Scope: llm.ScopeEndpoint}
}

// loginFailure turns a refusal from Google into an error. Its body is an OAuth error
// ({"error": "invalid_grant", "error_description": ...}) or a Google API one ({"error": {...}}).
func loginFailure(status int, body []byte) *llm.Error {
	var b struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	_ = json.Unmarshal(body, &b)
	var code string
	if json.Unmarshal(b.Error, &code) != nil {
		var o struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b.Error, &o)
		code, b.Description = o.Status, o.Message
	}
	msg := fmt.Sprintf("Google answered %d", status)
	if code != "" {
		msg += " (" + code + ")"
	}
	if b.Description != "" {
		msg += ": " + b.Description
	}
	switch {
	case code == "invalid_grant" || code == "invalid_client" || code == "unauthorized_client":
		return &llm.Error{Code: llm.CodeAuth, Message: "Google refused the sign-in (" + code + "); log in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &llm.Error{Code: llm.CodeAuth, Message: msg + "; log in again", Status: status, ProviderCode: code, Scope: llm.ScopeCredential}
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: msg, Status: status, ProviderCode: code, Retryable: true, Scope: llm.ScopeCredential}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: msg, Status: status, Retryable: true, Scope: llm.ScopeEndpoint}
	}
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Status: status, ProviderCode: code, Scope: llm.ScopeEndpoint}
}

func refused(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

// randomString returns n random bytes, base64url encoded without padding.
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
