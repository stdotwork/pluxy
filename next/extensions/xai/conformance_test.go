package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with a fake xAI that
// acts out each scenario, for an API key and for a Grok login.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	t.Run("api_key", func(t *testing.T) {
		conformance.Run(t, conformance.Target{Manifest: manifest, Prepare: prepare(false), PrepareLogin: prepareLogin})
	})
	t.Run("login", func(t *testing.T) {
		conformance.Run(t, conformance.Target{Manifest: manifest, Prepare: prepare(true), PrepareLogin: prepareLogin})
	})
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := xaitest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings()}
	if method == "api_key" {
		var key string
		key = srv.Key()
		if s == conformance.LoginDenied {
			key = "made-up-rejected-key"
		}
		return conformance.LoginSetup{Account: account, Input: map[string]string{"api_key": key}}
	}

	approve := func(t *testing.T, p protocol.Prompt) map[string]string {
		if !srv.Approve(p.UserCode) {
			t.Errorf("no device login shows code %q", p.UserCode)
		}
		return nil
	}
	switch s {
	case conformance.LoginApproved:
		return conformance.LoginSetup{Account: account, Act: approve}
	case conformance.LoginDenied:
		srv.DenyDevice = true
		return conformance.LoginSetup{Account: account, Act: func(*testing.T, protocol.Prompt) map[string]string { return nil }}
	case conformance.LoginAbandoned:
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRenewed, conformance.RefreshUnavailable:
		srv.TokenDown = s == conformance.RefreshUnavailable
		access, refresh := srv.Grant()
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh}
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRejected:
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "xaitest-access-gone", RefreshToken: "xaitest-refresh-not-issued"}
		return conformance.LoginSetup{Account: account}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}

func prepare(login bool) func(t *testing.T, s conformance.Scenario) conformance.Setup {
	return func(t *testing.T, s conformance.Scenario) conformance.Setup { return prepareCall(t, s, login) }
}

func prepareCall(t *testing.T, s conformance.Scenario, login bool) conformance.Setup {
	t.Helper()
	srv := xaitest.New(t)
	cancelled := make(chan struct{})
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req xaitest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			if r.Header.Get("Accept") == "text/event-stream" {
				xaitest.StreamText(w, text[:6], text[6:])
				return
			}
			xaitest.JSONText(w, text)
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_hang\",\"object\":\"response\",\"created_at\":1700000000,\"status\":\"in_progress\",\"model\":\"grok-test\",\"output\":[]}}\n\n")
			io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_hang\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n")
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_hang\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hold on\"}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-time.After(30 * time.Second):
			}
		case conformance.ScenarioRateLimited:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"code":"Too many requests","error":"Rate limit reached for requests"}`)
		}
	}
	cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.Key()}
	if login {
		access, refresh := srv.Grant()
		cred = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh}
	}
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(cred.APIKey + cred.AccessToken)
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(), Credential: cred}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}
