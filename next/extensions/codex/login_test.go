package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A renewed subscription keeps its ChatGPT account, takes the rotated refresh token, and is due
// for renewal four days before its access token expires.
func TestRefreshRotatesAndSchedulesRenewal(t *testing.T) {
	srv := codextest.New(t)
	srv.AccessTTL = func(int) time.Duration { return 10 * 24 * time.Hour }
	access, refreshToken, _ := srv.Grant()
	old := subscription(access, refreshToken)
	got, err := refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, old)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != protocol.CredentialOAuth || got.AccessToken == "" || got.AccessToken == access {
		t.Errorf("access token not renewed: %+v", got)
	}
	if got.RefreshToken == "" || got.RefreshToken == refreshToken {
		t.Errorf("refresh token %q, want the rotated one", got.RefreshToken)
	}
	if want := got.ExpiresAt - 4*24*60*60; got.ExpiresAt == 0 || got.RefreshAt != want {
		t.Errorf("refresh_at = %d, want %d (expires_at %d minus four days)", got.RefreshAt, want, got.ExpiresAt)
	}
	var d credentialData
	_ = json.Unmarshal(got.Data, &d)
	if d.AccountID != codextest.AccountID || d.IDToken == "" {
		t.Errorf("data = %s, want account %s and an ID token", got.Data, codextest.AccountID)
	}
	// The old refresh token was used up.
	if _, err := refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, old)}); err == nil {
		t.Error("a used refresh token was accepted again")
	}
}

// A token reply without a refresh token or ID token keeps the ones the credential had.
func TestRenewalKeepsTokensTheServerDidNotSend(t *testing.T) {
	old := subscription("old-access", "old-refresh")
	g, err := grantFrom(&tokenResponse{AccessToken: "new-access", ExpiresIn: 3600}, old)
	if err != nil {
		t.Fatal(err)
	}
	c := g.Credential
	if c.AccessToken != "new-access" || c.RefreshToken != "old-refresh" {
		t.Errorf("tokens = %q, %q; want new-access, old-refresh", c.AccessToken, c.RefreshToken)
	}
	var d credentialData
	_ = json.Unmarshal(c.Data, &d)
	if d.IDToken != codextest.IDToken() || d.AccountID != codextest.AccountID {
		t.Errorf("data = %s, want the old ID token and account", c.Data)
	}
	// An hour-long token is renewed by core's default lead, not four days ahead.
	if c.RefreshAt != 0 {
		t.Errorf("refresh_at = %d for an hour-long token, want 0", c.RefreshAt)
	}
}

// A login server reply that redirects is not followed: following a 307 would send the refresh
// token (or an authorization code and its verifier) to wherever the redirect points.
func TestTokenEndpointRedirectIsNotFollowed(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer elsewhere.Close()
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/oauth/token", http.StatusTemporaryRedirect)
	}))
	defer login.Close()
	settings, _ := json.Marshal(map[string]any{"auth_base_url": login.URL})
	acct := protocol.Account{ID: "codex", Settings: settings, Credential: subscription("access", "secret-refresh")}
	if _, err := refresh(context.Background(), sdk.RefreshCall{Account: acct}); err == nil {
		t.Error("a redirected refresh succeeded")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect target got %d requests carrying the refresh token", n)
	}
}

// A sign-in result whose state is not this login's is ignored, even when it reports an error, so
// another tab or an earlier login cannot end this one; the callback listener does not pass such a
// result on.
func TestSignInResultChecksStateFirst(t *testing.T) {
	const state = "state-of-this-login"
	for _, tc := range []struct {
		query    string
		ok       bool
		code     string
		refusing bool
	}{
		{query: "error=access_denied&state=other"},
		{query: "error=access_denied"},
		{query: "code=c&state=other"},
		{query: "error=access_denied&state=" + state, ok: true, refusing: true},
		{query: "state=" + state, ok: true, refusing: true},
		{query: "code=c&state=" + state, ok: true, code: "c"},
	} {
		params, _ := url.ParseQuery(tc.query)
		code, ok, err := signInResult(params, state)
		if ok != tc.ok || code != tc.code || (err != nil) != tc.refusing {
			t.Errorf("%s: code %q, ok %v, err %v", tc.query, code, ok, err)
		}
	}

	results := make(chan url.Values, 1)
	h := callbackHandler(state, results)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, callbackPath+"?error=access_denied&state=other", nil))
	if rec.Code != http.StatusBadRequest || len(results) != 0 {
		t.Errorf("unrelated callback: status %d, %d results passed on", rec.Code, len(results))
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, callbackPath+"?code=c&state="+state, nil))
	if rec.Code != http.StatusOK || len(results) != 1 {
		t.Errorf("this login's callback: status %d, %d results passed on", rec.Code, len(results))
	}
}

// The goroutine that forwards login input ends once the login's context is cancelled, even with
// input it cannot hand over: nothing reads the inputs after the login ends.
func TestForwardInputsStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := func(ctx context.Context) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return map[string]string{"redirect_url": "x"}, nil
	}
	forwardInputs(ctx, input)
	// Let the forwarder fill the inputs and block handing over the next one.
	time.Sleep(50 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		if !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "forwardInputs.func1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the input forwarder still runs after the login was cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
