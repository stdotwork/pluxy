package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A setup token kept as an API key goes upstream as an OAuth token: a bearer token, with the OAuth
// beta after the client's own betas, and no x-api-key.
func TestSetupTokenIsSentAsOAuthToken(t *testing.T) {
	srv := claudetest.New(t)
	token := srv.SetupToken()
	s, err := readSettings(srv.Settings(""))
	if err != nil {
		t.Fatal(err)
	}
	call := sdk.Call{
		Account: protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: token}},
		Request: llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages,
			Headers: map[string]string{"anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14"}}},
	}
	resp, err := post(context.Background(), call, s, "/v1/messages", []byte(`{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`), true, false)
	if err != nil {
		t.Fatalf("the fake refused the request: %v", err)
	}
	resp.Body.Close()
	got := srv.Requests()[0].Header
	if got.Get("Authorization") != "Bearer "+token || got.Get("X-Api-Key") != "" {
		t.Errorf("Authorization = %q, X-Api-Key = %q; want the token as a bearer token only", got.Get("Authorization"), got.Get("X-Api-Key"))
	}
	if v := got.Get("Anthropic-Beta"); v != "claude-code-20250219,interleaved-thinking-2025-05-14,"+oauthBeta {
		t.Errorf("Anthropic-Beta = %q, want the client's betas, then the OAuth beta", v)
	}
}

// The setup_token login keeps the token as an API key credential without a request: nothing
// checks such a token for free. An API key login given a setup token keeps it the same way.
func TestSetupTokenLoginKeepsTokenUnchecked(t *testing.T) {
	srv := claudetest.New(t)
	for _, method := range []string{"setup_token", "api_key"} {
		token := srv.SetupToken()
		g, err := login(context.Background(), sdk.LoginCall{Method: method, Account: protocol.Account{ID: "a", Settings: srv.Settings("")},
			Input: map[string]string{method: "  " + token + "\n"}}, nil)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if c := g.Credential; c == nil || c.Kind != protocol.CredentialAPIKey || c.APIKey != token || c.RefreshToken != "" || c.ExpiresAt != 0 {
			t.Errorf("%s: credential = %+v, want the trimmed token as an API key", method, g.Credential)
		}
	}
	if n := srv.ModelRequests() + len(srv.OAuthRequests()) + len(srv.Requests()); n != 0 {
		t.Errorf("the logins sent %d requests, want none", n)
	}
}

// The setup_token login asks again for an empty value, and refuses a value that is not an OAuth
// token, such as an API key, as an API key login refuses a bad key.
func TestSetupTokenLoginRefusesOtherValues(t *testing.T) {
	for in, code := range map[string]string{"": llm.CodeInvalidRequest, "  ": llm.CodeInvalidRequest, "sk-ant-api03-not-a-setup-token": llm.CodeAuth} {
		_, err := setupTokenLogin(in)
		if e := llmError(t, err); e.Code != code || strings.Contains(e.Message, "not-a-setup-token") {
			t.Errorf("%q: error = %+v, want code %s without the value", in, e, code)
		}
	}
}

// The setup_token method is offered as a key entry with one secret input.
func TestSetupTokenLoginMethod(t *testing.T) {
	for _, m := range loginMethods {
		if m.Method == "setup_token" {
			if m.Kind != protocol.LoginAPIKey || len(m.Inputs) != 1 || m.Inputs[0].Name != "setup_token" || !m.Inputs[0].Secret {
				t.Errorf("method = %+v, want an api_key kind with one secret setup_token input", m)
			}
			return
		}
	}
	t.Error("no setup_token login method")
}

// A setup token is not checked: no free call is known for one, so nothing is sent.
func TestCheckSetupTokenIsUnsupportedWithoutRequest(t *testing.T) {
	srv := claudetest.New(t)
	_, err := runCheck(srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.SetupToken()})
	if e := llmError(t, err); e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error = %+v, want unsupported on the credential", e)
	}
	if n := srv.ModelRequests() + len(srv.Requests()); n != 0 {
		t.Errorf("the upstream got %d requests, want none", n)
	}
}

// A setup token's quota reads the subscription's usage as an OAuth token, and skips the profile,
// which such a token cannot read: the usage is reported without a plan.
func TestQuotaSetupTokenReadsUsageOnly(t *testing.T) {
	srv := claudetest.New(t)
	token := srv.SetupToken()
	q, e := readQuota(t, protocol.Account{ID: "claude", Settings: srv.Settings(""),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: token}})
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	reqs := srv.OAuthRequests()
	if len(reqs) != 1 || reqs[0].Prefix != "/api/oauth/usage" {
		t.Fatalf("requests = %+v, want only GET /api/oauth/usage", reqs)
	}
	if h := reqs[0].Header; h.Get("Authorization") != "Bearer "+token || h.Get("Anthropic-Beta") != oauthBeta || h.Get("X-Api-Key") != "" {
		t.Errorf("usage request headers = %v, want the token as a bearer token with the OAuth beta", h)
	}
	if q.Plan != "" || len(q.Limits) == 0 {
		t.Errorf("quota = %+v, want the usage limits and no plan", q)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(q.Raw, &raw); err != nil || len(raw["usage"]) == 0 || raw["profile"] != nil {
		t.Errorf("raw = %s (%v), want the usage reply only", q.Raw, err)
	}
}
