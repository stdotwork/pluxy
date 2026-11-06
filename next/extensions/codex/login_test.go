package main

import (
	"context"
	"encoding/json"
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

func TestThinkingSuffix(t *testing.T) {
	for _, tc := range []struct{ in, model, effort string }{
		{"gpt-6.1-sol(high)", "gpt-6.1-sol", "high"},
		{"gpt-6.1-sol(XHigh)", "gpt-6.1-sol", "xhigh"},
		{"gpt-6.1-sol(8192)", "gpt-6.1-sol", "medium"},
		{"gpt-6.1-sol(0)", "gpt-6.1-sol", "none"},
		{"gpt-6.1-sol(100000)", "gpt-6.1-sol", "xhigh"},
		{"gpt-6.1-sol(fast)", "gpt-6.1-sol(fast)", ""},
		{"gpt-6.1-sol", "gpt-6.1-sol", ""},
	} {
		if m, e := splitThinkingSuffix(tc.in); m != tc.model || e != tc.effort {
			t.Errorf("splitThinkingSuffix(%q) = %q, %q; want %q, %q", tc.in, m, e, tc.model, tc.effort)
		}
	}
}
