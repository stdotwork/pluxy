package cred

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

func grantLabelled(label string, identity map[string]string) *protocol.Grant {
	return &protocol.Grant{Label: label, Identity: identity, Credential: &protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: "access-" + label, RefreshToken: "refresh-" + label, ExpiresAt: time.Now().Add(time.Hour).Unix()}}
}

func loginManager(t *testing.T, p *immediateLoginProvider) *Manager {
	t.Helper()
	store, err := Open(FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, single{p}, Options{})
	t.Cleanup(m.Close)
	return m
}

func runLoginTo(t *testing.T, m *Manager, req LoginRequest) LoginView {
	t.Helper()
	req.Method = "device"
	v, err := m.StartLogin(context.Background(), req)
	if err != nil {
		t.Fatalf("StartLogin(%+v): %v", req, err)
	}
	return v
}

// A label the caller gives a new account wins over the provider's. Signing in to the account
// again keeps it, and the new login is still checked against the provider's label when the
// provider reports no identity: the same upstream account (letter case aside) is let in, another
// is refused. Without a label from the caller, the provider's is the account's, and follows a new
// login's, as before.
func TestCallerLabelWinsAndReloginChecksProviderLabel(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)

	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "work"}, Label: "Work laptop"}); v.State != LoginDone {
		t.Fatalf("login ended %+v", v)
	}
	if r, _ := m.store.Get("work"); r.Label != "Work laptop" {
		t.Fatalf("label = %q, want the caller's", r.Label)
	}

	p.grant = grantLabelled("JANE@example.test", nil)
	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "work"}, Reauth: true}); v.State != LoginDone {
		t.Fatalf("a new login to the same upstream account ended %s: %+v", v.State, v.Error)
	}
	if r, _ := m.store.Get("work"); r.Label != "Work laptop" || r.Credential.AccessToken != "access-JANE@example.test" {
		t.Fatalf("after the new login: label %q, token %q", r.Label, r.Credential.AccessToken)
	}

	p.grant = grantLabelled("someone@example.test", nil)
	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "work"}, Reauth: true}); v.State != LoginFailed || v.Error == nil {
		t.Fatalf("a new login to another upstream account ended %s, want failed", v.State)
	}

	p.grant = grantLabelled("bob@example.test", nil)
	runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "plain"}})
	p.grant = grantLabelled("BOB@example.test", nil)
	runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "plain"}, Reauth: true})
	if r, _ := m.store.Get("plain"); r.Label != "BOB@example.test" {
		t.Fatalf("an account without the caller's label is labelled %q, want the provider's latest", r.Label)
	}
}
