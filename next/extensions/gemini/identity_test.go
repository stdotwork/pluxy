package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func loginWith(t *testing.T, srv *geminitest.Server, method string, input map[string]string) *protocol.Grant {
	t.Helper()
	g, err := login(context.Background(), sdk.LoginCall{Account: protocol.Account{ID: "g", Settings: settingsFor(srv, kindVertexSA)}, Method: method, Input: input}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A service account login names who signed in by the key's client_email, which differs between
// accounts; an API key login has no identity.
func TestLoginIdentity(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	key1, key2 := srv.ServiceAccount("proj-1", true), srv.ServiceAccount("proj-1", true)
	g1 := loginWith(t, srv, "service_account", map[string]string{"service_account": string(key1)})
	g2 := loginWith(t, srv, "service_account", map[string]string{"service_account": string(key2)})

	var sa serviceAccount
	_ = json.Unmarshal(key1, &sa)
	if len(g1.Identity) != 1 || sa.ClientEmail == "" || g1.Identity["client_email"] != sa.ClientEmail {
		t.Errorf("identity %v, want client_email %s", g1.Identity, sa.ClientEmail)
	}
	if g1.Identity["client_email"] == g2.Identity["client_email"] {
		t.Error("two service accounts have the same identity")
	}

	api := loginWith(t, srv, "api_key", map[string]string{"api_key": srv.IssueKey()})
	if len(api.Identity) != 0 {
		t.Errorf("an API key has identity %v", api.Identity)
	}
}
