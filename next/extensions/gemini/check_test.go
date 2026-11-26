package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func runCheck(settings json.RawMessage, cred *protocol.Credential) (*protocol.CheckResult, error) {
	return check(context.Background(), sdk.CheckCall{Account: protocol.Account{ID: "a", Settings: settings, Credential: cred}})
}

func checkError(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *llm.Error", err)
	}
	return e
}

func geminiSettings(srv *geminitest.Server) json.RawMessage {
	return json.RawMessage(`{"base_url":"` + srv.URL + `"}`)
}

// A good API key is checked with the model list and the result names how many models it lists.
func TestCheckAPIKeyListsModels(t *testing.T) {
	srv := geminitest.New(t)
	res, err := runCheck(geminiSettings(srv), &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()})
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
		t.Errorf("the check sent %d GenerateContent requests, want none", n)
	}
}

// A refused key (Google answers 400 API_KEY_INVALID) is an auth error that does not repeat the key.
func TestCheckRefusedKey(t *testing.T) {
	srv := geminitest.New(t)
	const key = "geminitest-key-not-issued"
	_, err := runCheck(geminiSettings(srv), &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
	if e := checkError(t, err); e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || strings.Contains(e.Message, key) {
		t.Errorf("error = %+v, want auth on the credential without the key", e)
	}
}

// A Vertex account, with an API key or a service account, is answered unsupported without any
// request to the upstream.
func TestCheckVertexIsUnsupportedWithoutRequest(t *testing.T) {
	srv := geminitest.New(t)
	settings := json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`)
	for name, cred := range map[string]*protocol.Credential{
		"api key":         {Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
		"service account": {Kind: CredentialServiceAccount, Data: srv.ServiceAccount("p", true)},
	} {
		_, err := runCheck(settings, cred)
		if e := checkError(t, err); e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
			t.Errorf("%s: error = %+v, want unsupported on the credential", name, e)
		}
	}
	if n := srv.ModelRequests() + len(srv.Requests()) + srv.Grants(); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

// An upstream that cannot be reached is unavailable on the endpoint.
func TestCheckUnreachable(t *testing.T) {
	srv := geminitest.New(t)
	key := srv.IssueKey()
	srv.Close()
	_, err := runCheck(geminiSettings(srv), &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
	if e := checkError(t, err); e.Code != llm.CodeUnavailable || e.Scope != llm.ScopeEndpoint {
		t.Errorf("error = %+v, want unavailable on the endpoint", e)
	}
}
