package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// An account's model list is read once, and again only when the account's credential or
// settings change; a failure is not kept.
func TestCatalogCache(t *testing.T) {
	calls := 0
	fail := false
	c := (&catalogs{}).cached(func(_ context.Context, ext string, a protocol.Account) (*protocol.ModelList, *llm.Error) {
		calls++
		if fail {
			return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "down"}
		}
		return &protocol.ModelList{Models: []protocol.Model{{ID: ext + "/" + a.ID + "/" + a.Credential.APIKey}}}, nil
	})
	ctx := context.Background()
	a := route.Account{ID: "a", Extension: "x", Settings: json.RawMessage(`{"base_url":"u1"}`)}
	key := func(k string) *protocol.Credential {
		return &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: k}
	}
	read := func(a route.Account, cred *protocol.Credential) string {
		t.Helper()
		l, err := c(ctx, a, cred)
		if err != nil {
			return "error"
		}
		return l.Models[0].ID
	}

	if got := read(a, key("k1")); got != "x/a/k1" || calls != 1 {
		t.Fatalf("first read %q, %d calls", got, calls)
	}
	if got := read(a, key("k1")); got != "x/a/k1" || calls != 1 {
		t.Errorf("same credential: %q, %d calls; want the kept list", got, calls)
	}
	if got := read(a, key("k2")); got != "x/a/k2" || calls != 2 {
		t.Errorf("renewed credential: %q, %d calls; want a new read", got, calls)
	}
	b := a
	b.Settings = json.RawMessage(`{"base_url":"u2"}`)
	if read(b, key("k2")); calls != 3 {
		t.Errorf("changed settings: %d calls; want a new read", calls)
	}
	fail = true
	if got := read(a, key("k3")); got != "error" || calls != 4 {
		t.Errorf("failed read: %q, %d calls", got, calls)
	}
	fail = false
	if got := read(a, key("k3")); got != "x/a/k3" || calls != 5 {
		t.Errorf("after a failure: %q, %d calls; want a new read", got, calls)
	}
}
