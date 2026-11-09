package main

// Sign in with ChatGPT (SIWC): a ChatGPT sign-in made for agents other than the Codex client,
// whose token calls the OpenAI API (https://api.openai.com/v1) on the user's ChatGPT plan. Today's
// core keeps it as a second token set inside a Codex subscription's auth file and binds it
// through its management API; here it is a login method of its own ("siwc") that makes its own
// account. The login, its renewal and the request rules follow today's core
// (core/internal/auth/codex/siwc_auth.go, core/internal/runtime/executor/codex_executor_request.go).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	// siwcClientID asks the login server to register a client for this agent; the client ID it
	// issues comes back with the sign-in result, and the code exchange and every renewal use it.
	siwcClientID      = "dynamic_agent_client"
	siwcAuthorizePath = "/api/accounts/authorize"
	siwcTokenPath     = "/api/accounts/oauth/token"
	// siwcResource is the API the tokens are for.
	siwcResource = "https://api.openai.com/v1"
	// siwcRedirectURI is served on the same address and path as the browser login's redirect
	// (callbackAddr, callbackPath), under the host name SIWC registers.
	siwcRedirectURI = "http://127.0.0.1:1455/auth/callback"
	// siwcDirectScope lets the token call the OpenAI API directly; a grant without it is useless.
	siwcDirectScope = "chatgpt.tokens.use.direct"
	siwcScope       = "openid profile email offline_access resource.invoke " + siwcDirectScope
	// siwcRefreshLead is how long before its access token expires a SIWC credential is renewed,
	// as today's core renews it.
	siwcRefreshLead = 5 * time.Minute
	// defaultSIWCIdentifier names this program to a SIWC sign-in. Today's core says
	// "CLIProxyAPI", its own name.
	defaultSIWCIdentifier = "pluxy"
)

// siwcIdentifier is the signin_with_chatgpt_identifier setting: the agent name the sign-in page
// shows, and the start of the requests' User-Agent.
func (s settings) siwcIdentifier() string {
	if v := strings.TrimSpace(s.SIWCIdentifier); v != "" {
		return v
	}
	return defaultSIWCIdentifier
}

// siwcUserAgent is the User-Agent of a SIWC account's requests.
func siwcUserAgent(s settings) string {
	return fmt.Sprintf("%s (%s; %s)", s.siwcIdentifier(), runtime.GOOS, runtime.GOARCH)
}

// siwcLogin signs in with ChatGPT: an authorization code flow with PKCE, a nonce and this
// device's ID, whose result also carries the client ID the login server issued.
func siwcLogin(ctx context.Context, set settings, s *sdk.LoginSession) (*protocol.Grant, error) {
	verifier := randomString(64)
	state := randomString(32)
	deviceID := randomUUID()
	authURL := siwcAuthURL(set, verifier, state, randomString(32), deviceID)
	params, err := awaitRedirect(ctx, set, s, authURL, state,
		"Open the link and sign in with ChatGPT to let "+set.siwcIdentifier()+" use your plan. If the page you end on does not load, paste its address here.")
	if err != nil {
		return nil, err
	}
	return siwcExchange(ctx, set, params, verifier, deviceID)
}

// siwcAuthURL is the sign-in link of a SIWC login.
func siwcAuthURL(set settings, verifier, state, nonce, deviceID string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {siwcClientID},
		"response_type":         {"code"},
		"agent_name_hint":       {set.siwcIdentifier()},
		"ext_agent_host_id":     {"urn:uuid:" + deviceID},
		"redirect_uri":          {siwcRedirectURI},
		"resource":              {siwcResource},
		"scope":                 {siwcScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"nonce":                 {nonce},
	}
	return strings.TrimRight(set.authBase(), "/") + siwcAuthorizePath + "?" + q.Encode()
}

// siwcExchange trades a SIWC sign-in result (params, the query the browser came back with) for
// the account's credential, as the client the login server issued.
func siwcExchange(ctx context.Context, set settings, params url.Values, verifier, deviceID string) (*protocol.Grant, error) {
	issued := strings.TrimSpace(params.Get("client_id"))
	if issued == "" {
		return nil, refused("the sign-in result has no issued client ID; sign in again")
	}
	tr, err := siwcToken(ctx, set, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {issued},
		"code":          {params.Get("code")},
		"code_verifier": {verifier},
		"redirect_uri":  {siwcRedirectURI},
		"resource":      {siwcResource},
	}, true)
	if err != nil {
		return nil, err
	}
	return siwcGrant(tr, nil, issued, deviceID), nil
}

// siwcRefresh renews a SIWC credential with its refresh token and issued client ID.
func siwcRefresh(ctx context.Context, set settings, c *protocol.Credential, d credentialData) (*protocol.Credential, error) {
	if d.ClientID == "" {
		return nil, refused("the SIWC credential has no issued client ID; sign in again")
	}
	tr, err := siwcToken(ctx, set, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {d.ClientID},
		"refresh_token": {c.RefreshToken},
		"resource":      {siwcResource},
	}, false)
	if err != nil {
		return nil, err
	}
	return siwcGrant(tr, c, d.ClientID, d.DeviceID).Credential, nil
}

// siwcToken asks the token endpoint for a SIWC grant and checks it as today's core does: tokens,
// scope and lifetime present, an ID token after a sign-in (a renewal need not send one), and the
// scope that lets the token call the API.
func siwcToken(ctx context.Context, set settings, form url.Values, signIn bool) (*tokenResponse, error) {
	tr, err := tokenRequest(ctx, set, siwcTokenPath, form)
	if err != nil {
		return nil, err
	}
	switch {
	case tr.RefreshToken == "" || strings.TrimSpace(tr.Scope) == "" || tr.ExpiresIn <= 0 || signIn && tr.IDToken == "":
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the token endpoint sent an incomplete SIWC grant", Scope: llm.ScopeEndpoint}
	case !slices.Contains(strings.Fields(tr.Scope), siwcDirectScope):
		return nil, refused("the grant does not include " + siwcDirectScope + ", which a SIWC sign-in needs")
	}
	return tr, nil
}

// siwcGrant builds a SIWC credential from a token reply. old is the credential being renewed,
// whose ID token stays when the reply has none.
func siwcGrant(tr *tokenResponse, old *protocol.Credential, clientID, deviceID string) *protocol.Grant {
	c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	var d credentialData
	if old != nil && len(old.Data) > 0 {
		_ = json.Unmarshal(old.Data, &d)
	}
	d.SIWC, d.ClientID, d.DeviceID, d.Scope = true, clientID, deviceID, strings.TrimSpace(tr.Scope)
	if tr.IDToken != "" {
		d.IDToken = tr.IDToken
	}
	c.Data = raw(d)
	now := time.Now()
	c.ExpiresAt = sdk.ExpiryUnix(now, tr.ExpiresIn)
	if c.ExpiresAt > 0 {
		if at := time.Unix(c.ExpiresAt, 0).Add(-siwcRefreshLead); at.After(now) {
			c.RefreshAt = at.Unix()
		}
	}
	claims, _ := parseIDToken(d.IDToken)
	identity := map[string]string{}
	if claims.email != "" {
		identity["email"] = claims.email
	}
	if claims.accountID != "" {
		identity["account_id"] = claims.accountID
	}
	return &protocol.Grant{Credential: c, Label: claims.email, Identity: identity}
}

// randomUUID returns a random (version 4) UUID in lower case: the device ID a SIWC sign-in names
// its host by.
func randomUUID() string {
	u := make([]byte, 16)
	_, _ = rand.Read(u)
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// isSIWC reports whether c is a SIWC credential.
func isSIWC(c *protocol.Credential) bool {
	if c == nil || len(c.Data) == 0 {
		return false
	}
	var d credentialData
	_ = json.Unmarshal(c.Data, &d)
	return d.SIWC
}

// codexClientAgents are what the User-Agent of a Codex client contains, as today's core tells
// them apart (core/sdk/cliproxy/auth/types.go, IsCodexClientUserAgent).
var codexClientAgents = []string{"codex-desktop", "codex desktop", "codex-rs", "codex_cli_rs", "codex-tui", "codex_exec"}

// codexClient reports whether req comes from a Codex client.
func codexClient(req llm.Request) bool {
	if req.Native == nil {
		return false
	}
	ua := strings.ToLower(req.Native.Headers["user-agent"])
	return slices.ContainsFunc(codexClientAgents, func(s string) bool { return strings.Contains(ua, s) })
}

// siwcUnsupported refuses a request a SIWC account does not serve, so that core tries another
// account. Today's core serves a Codex client's requests on such a record with the
// subscription's own token, which a SIWC account does not have.
func siwcUnsupported(what string) error {
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: "a Sign in with ChatGPT (SIWC) account does not serve " + what}
}

// siwcCodexClient is the refusal of a Codex client's request on a SIWC account.
func siwcCodexClient() error {
	return siwcUnsupported("Codex clients, which need a ChatGPT subscription's sign-in")
}

// siwcBody applies today's SIWC body rules to a Responses request that has had the Codex shape:
// the OpenAI API refuses configuration_update input items on a SIWC token, so they become the
// request's reasoning effort (a thinking suffix's effort, suffix true, wins and the items are
// only removed); the fields it does not take are removed; and blank instructions are left out
// rather than sent empty.
func siwcBody(m map[string]json.RawMessage, suffix bool) {
	collapseConfigurationUpdates(m, suffix)
	for _, k := range []string{"prompt_cache_retention", "prompt_cache_options", "max_completion_tokens", "temperature"} {
		delete(m, k)
	}
	var inst string
	if v, ok := m["instructions"]; ok && (string(v) == "null" || json.Unmarshal(v, &inst) == nil && strings.TrimSpace(inst) == "") {
		delete(m, "instructions")
	}
}

// collapseConfigurationUpdates removes the configuration_update items of a list input and,
// unless keepEffort, sets reasoning.effort to the last of their non-empty efforts, as today's
// core does (core/internal/thinking/configuration_update.go).
func collapseConfigurationUpdates(m map[string]json.RawMessage, keepEffort bool) {
	var items []json.RawMessage
	if json.Unmarshal(m["input"], &items) != nil {
		return
	}
	kept := items[:0:0]
	effort := ""
	for _, it := range items {
		var o struct {
			Type      string `json:"type"`
			Reasoning struct {
				Effort json.RawMessage `json:"effort"`
			} `json:"reasoning"`
		}
		if json.Unmarshal(it, &o) != nil || o.Type != "configuration_update" {
			kept = append(kept, it)
			continue
		}
		var e string
		if json.Unmarshal(o.Reasoning.Effort, &e) == nil {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				effort = e
			}
		}
	}
	if len(kept) == len(items) {
		return
	}
	m["input"] = raw(kept)
	if effort == "" || keepEffort {
		return
	}
	var r map[string]json.RawMessage
	_ = json.Unmarshal(m["reasoning"], &r)
	if r == nil {
		r = map[string]json.RawMessage{}
	}
	r["effort"] = raw(effort)
	m["reasoning"] = raw(r)
}
