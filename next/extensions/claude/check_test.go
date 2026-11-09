package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func runCheck(srv *claudetest.Server, cred *protocol.Credential) (*protocol.CheckResult, error) {
	return check(context.Background(), sdk.CheckCall{Account: protocol.Account{ID: "a", Settings: srv.Settings(""), Credential: cred}})
}

func llmError(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *llm.Error", err)
	}
	return e
}

// A good API key is checked with the model list and the result names how many models it lists.
func TestCheckAPIKeyListsModels(t *testing.T) {
	srv := claudetest.New(t)
	res, err := runCheck(srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Detail != "the upstream lists 1 models" {
		t.Errorf("result = %+v, want the upstream lists 1 models", res)
	}
	if n := srv.ModelRequests(); n != 1 {
		t.Errorf("model list requests = %d, want 1", n)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("the check sent %d Messages requests, want none", n)
	}
}

// A refused key is an auth error that does not repeat the key.
func TestCheckRefusedKey(t *testing.T) {
	srv := claudetest.New(t)
	const key = "sk-ant-api-test-not-issued"
	_, err := runCheck(srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
	if e := llmError(t, err); e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || strings.Contains(e.Message, key) {
		t.Errorf("error = %+v, want auth on the credential without the key", e)
	}
}

// A subscription login is answered unsupported without any request to the upstream.
func TestCheckLoginIsUnsupportedWithoutRequest(t *testing.T) {
	srv := claudetest.New(t)
	access, refresh := srv.Grant()
	_, err := runCheck(srv, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh})
	if e := llmError(t, err); e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error = %+v, want unsupported on the credential", e)
	}
	if n := srv.ModelRequests() + len(srv.Requests()); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

// An upstream that cannot be reached is unavailable on the endpoint.
func TestCheckUnreachable(t *testing.T) {
	srv := claudetest.New(t)
	key := srv.IssueKey()
	srv.Close()
	_, err := runCheck(srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
	if e := llmError(t, err); e.Code != llm.CodeUnavailable || e.Scope != llm.ScopeEndpoint {
		t.Errorf("error = %+v, want unavailable on the endpoint", e)
	}
}
