package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with a fake Kimi
// that acts out each scenario,
// for each kind of account: a device-code login and a Kimi Code API key.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	for _, apiKey := range []bool{false, true} {
		name := "login"
		if apiKey {
			name = "api-key"
		}
		t.Run(name, func(t *testing.T) {
			conformance.Run(t, conformance.Target{
				Manifest:     manifest,
				Prepare:      func(t *testing.T, s conformance.Scenario) conformance.Setup { return prepare(t, s, apiKey) },
				PrepareLogin: prepareLogin,
			})
		})
	}
}

func prepare(t *testing.T, s conformance.Scenario, apiKey bool) conformance.Setup {
	t.Helper()
	srv := kimitest.New(t)
	cancelled := make(chan struct{})
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req kimitest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			if r.Header.Get("Accept") == "text/event-stream" {
				kimitest.StreamText(w, text[:6], text[6:])
				return
			}
			kimitest.JSONText(w, text)
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"id":"k","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hold on"},"finish_reason":null}]}`+"\n\n")
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
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_reached_error"}}`)
		}
	}
	access, refresh := srv.Grant()
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(access)
	}
	cred := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh, Data: []byte(`{"device_id":"conformance-device"}`)}
	if apiKey {
		// The fake accepts any token it issued, so an access token stands in for a key.
		cred = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: access}
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(), Credential: cred}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := kimitest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings()}
	switch s {
	case conformance.LoginApproved:
		return conformance.LoginSetup{Account: account, Act: func(t *testing.T, p protocol.Prompt) map[string]string {
			if !srv.Approve(p.UserCode) {
				t.Errorf("the prompt's user code %q is not one the server issued", p.UserCode)
			}
			return nil
		}}
	case conformance.LoginDenied:
		srv.Deny = true
		return conformance.LoginSetup{Account: account, Act: func(*testing.T, protocol.Prompt) map[string]string { return nil }}
	case conformance.LoginAbandoned:
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRenewed, conformance.RefreshUnavailable:
		srv.TokenDown = s == conformance.RefreshUnavailable
		access, refresh := srv.Grant()
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh}
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRejected:
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "kimitest-access-expired", RefreshToken: "kimitest-refresh-not-issued"}
		return conformance.LoginSetup{Account: account}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}
