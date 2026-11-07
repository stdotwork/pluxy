package main

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/meta/metatest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with a fake Meta
// acting out each scenario, for each kind of account: a Muse Code login and a Meta API key.
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
	srv := metatest.New(t)
	cancelled := make(chan struct{})
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req metatest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			metatest.StreamText(w, text[:6], text[6:])
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_hang\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n"+
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_hang\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"+
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_hang\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hold on\"}\n\n")
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
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limited"}}`)
		}
	}
	dca, key := srv.Grant()
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(key)
		srv.Revoke(dca)
	}
	cred := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca}
	if apiKey {
		cred = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(), Credential: cred}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := metatest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings()}
	switch s {
	case conformance.LoginApproved, conformance.LoginDenied:
		approve := s == conformance.LoginApproved
		switch method {
		case "device":
			srv.Deny = !approve
			return conformance.LoginSetup{Account: account, Act: func(t *testing.T, p protocol.Prompt) map[string]string {
				if approve && !srv.Approve(p.UserCode) {
					t.Errorf("the prompt's user code %q is not one the server issued", p.UserCode)
				}
				return nil
			}}
		case "api_key":
			// An API key is not checked when it is entered; an empty one is refused.
			key := ""
			if approve {
				key = "meta-conformance-key"
			}
			return conformance.LoginSetup{Account: account, Input: map[string]string{"api_key": key}}
		}
	case conformance.LoginAbandoned:
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRenewed, conformance.RefreshUnavailable:
		srv.MintDown = s == conformance.RefreshUnavailable
		dca, key := srv.Grant()
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca}
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRejected:
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "metatest-key-revoked", RefreshToken: "dca:metatest-not-issued"}
		return conformance.LoginSetup{Account: account}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}
