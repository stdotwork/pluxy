package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func apiKeyLoginCall(srv *codextest.Server, key string) (*protocol.Grant, error) {
	return login(context.Background(), sdk.LoginCall{Account: testAccount(srv, nil), Method: "api_key", Input: map[string]string{"api_key": key}}, nil)
}

func asLLMError(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *llm.Error", err)
	}
	return e
}

// A key the upstream issued is checked with the model list and kept as the account's credential.
func TestAPIKeyLoginKeepsAcceptedKey(t *testing.T) {
	srv := codextest.New(t)
	key := srv.IssueToken("text")
	g, err := apiKeyLoginCall(srv, " "+key+" ")
	if err != nil {
		t.Fatal(err)
	}
	if g.Credential == nil || g.Credential.Kind != protocol.CredentialAPIKey || g.Credential.APIKey != key {
		t.Errorf("credential = %+v, want the trimmed key", g.Credential)
	}
	n, headers := srv.ModelListRequests()
	if n != 1 || headers[0].Get("Authorization") != "Bearer "+key {
		t.Errorf("model list requests = %d %v, want one with the key", n, headers)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("the sign-in sent a model request: %+v", srv.Requests())
	}
}

// A key the provider refuses is not added, and the error does not repeat the key.
func TestAPIKeyLoginRefusesRefusedKey(t *testing.T) {
	srv := codextest.New(t)
	const key = "sk-test-not-issued"
	_, err := apiKeyLoginCall(srv, key)
	e := asLLMError(t, err)
	if e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || e.Message != "OpenAI refused the key" || strings.Contains(e.Message, key) {
		t.Errorf("error = %+v, want auth on the credential saying OpenAI refused the key", e)
	}
}

// With no base_url the check goes to OpenAI's default; here the default is replaced by a fake,
// so what matters is that a custom base_url is the address checked (the test above) and an empty
// key never reaches the upstream.
func TestAPIKeyLoginEmptyKeyIsNotSent(t *testing.T) {
	srv := codextest.New(t)
	_, err := apiKeyLoginCall(srv, "  ")
	if e := asLLMError(t, err); e.Code != llm.CodeAuth {
		t.Errorf("error = %+v, want a refusal", e)
	}
	if n, _ := srv.ModelListRequests(); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

// An upstream that cannot be reached is reported as that, not as a wrong key, and the account is
// not added.
func TestAPIKeyLoginUnreachable(t *testing.T) {
	srv := codextest.New(t)
	key := srv.IssueToken("text")
	srv.Close()
	g, err := apiKeyLoginCall(srv, key)
	if g != nil {
		t.Errorf("grant = %+v, want none", g)
	}
	if e := asLLMError(t, err); e.Code != llm.CodeUnavailable || e.Scope != llm.ScopeEndpoint {
		t.Errorf("error = %+v, want unavailable on the endpoint", e)
	}
}

// An upstream failure is reported as it is, without the key, and is not retried.
func TestAPIKeyLoginUpstreamFailure(t *testing.T) {
	const key = "sk-test-echoed"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway for ` + key + `"}}`))
	}))
	defer ts.Close()
	settings, _ := json.Marshal(map[string]any{"base_url": ts.URL})
	_, err := login(context.Background(), sdk.LoginCall{Account: protocol.Account{ID: "c", Settings: settings}, Method: "api_key", Input: map[string]string{"api_key": key}}, nil)
	e := asLLMError(t, err)
	if e.Code == llm.CodeAuth || e.Retryable || strings.Contains(e.Message, key) {
		t.Errorf("error = %+v, want a non-auth, non-retryable failure without the key", e)
	}
}

// An upstream with no model list cannot check a key: the key is kept as entered.
func TestAPIKeyLoginKeepsKeyWhereNoModelList(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	settings, _ := json.Marshal(map[string]any{"base_url": ts.URL + "/v1"})
	g, err := login(context.Background(), sdk.LoginCall{Account: protocol.Account{ID: "c", Settings: settings}, Method: "api_key", Input: map[string]string{"api_key": "any-key"}}, nil)
	if err != nil || g == nil || g.Credential.APIKey != "any-key" {
		t.Errorf("grant = %+v, err = %v, want the key kept", g, err)
	}
}
