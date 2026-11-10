package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with the fake
// OpenAI login server and Codex endpoint acting out each scenario on a ChatGPT subscription.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest:     manifest,
		Prepare:      prepare,
		PrepareLogin: prepareLogin,
	})
}

// testAccount returns an account whose model requests and login go to srv. The browser login does
// not listen for its redirect, so tests never bind its fixed port.
func testAccount(srv *codextest.Server, cred *protocol.Credential) protocol.Account {
	settings, _ := json.Marshal(map[string]any{"base_url": srv.URL + "/backend-api/codex", "auth_base_url": srv.URL, "callback_listen": false})
	return protocol.Account{ID: "codex", Settings: settings, Credential: cred}
}

func subscription(access, refresh string) *protocol.Credential {
	data, _ := json.Marshal(map[string]string{"id_token": codextest.IDToken(), "account_id": codextest.AccountID})
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh, Data: data}
}

func prepare(t *testing.T, s conformance.Scenario) conformance.Setup {
	t.Helper()
	srv := codextest.New(t)
	switch s {
	case conformance.ScenarioText, conformance.ScenarioRateLimited, conformance.ScenarioAuthFailed:
		return conformance.Setup{Account: testAccount(srv, subscription(srv.IssueToken(string(s)), ""))}
	case conformance.ScenarioHang:
		tok, cancelled := srv.IssueHang()
		return conformance.Setup{Account: testAccount(srv, subscription(tok, "")), UpstreamCancelled: cancelled}
	}
	t.Fatalf("no fixture for scenario %q", s)
	return conformance.Setup{}
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := codextest.New(t)
	switch s {
	case conformance.LoginApproved, conformance.LoginDenied:
		approve := s == conformance.LoginApproved
		switch method {
		case "device":
			return conformance.LoginSetup{Account: testAccount(srv, nil), Act: func(t *testing.T, p protocol.Prompt) map[string]string {
				if approve {
					srv.ApproveDevice(p.UserCode)
				} else {
					srv.DenyDevice(p.UserCode)
				}
				return nil
			}}
		case "browser", "siwc":
			srv.DenyBrowser = !approve
			return conformance.LoginSetup{Account: testAccount(srv, nil), Act: browserUser}
		case "api_key":
			// An API key is not checked when it is entered; an empty one is refused.
			key := ""
			if approve {
				key = "sk-conformance"
			}
			return conformance.LoginSetup{Account: testAccount(srv, nil), Input: map[string]string{"api_key": key}}
		}
	case conformance.LoginAbandoned:
		return conformance.LoginSetup{Account: testAccount(srv, nil)}
	case conformance.RefreshRenewed:
		access, refresh, _ := srv.Grant()
		return conformance.LoginSetup{Account: testAccount(srv, subscription(access, refresh))}
	case conformance.RefreshRejected:
		return conformance.LoginSetup{Account: testAccount(srv, subscription("expired-access-token", "rt-never-issued"))}
	case conformance.RefreshUnavailable:
		srv.RefreshFailure = "unavailable"
		access, refresh, _ := srv.Grant()
		return conformance.LoginSetup{Account: testAccount(srv, subscription(access, refresh))}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}

// browserUser plays the user of a browser login: it opens the sign-in URL without following the
// redirect and pastes back the address the browser was sent to.
func browserUser(t *testing.T, p protocol.Prompt) map[string]string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(p.URL)
	if err != nil {
		t.Fatalf("opening the sign-in URL: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		t.Fatalf("sign-in URL answered %d, want a redirect", resp.StatusCode)
	}
	return map[string]string{"redirect_url": loc}
}
