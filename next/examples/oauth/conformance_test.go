package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest:     manifest,
		Prepare:      prepare,
		PrepareLogin: prepareLogin,
	})
}

// account returns the account the extension is tested under: its settings point at base, and cred
// is the credential, if any.
func account(base string, cred *protocol.Credential) protocol.Account {
	settings, _ := json.Marshal(map[string]string{"base_url": base})
	return protocol.Account{ID: "oauth", Settings: settings, Credential: cred}
}

// oauthCredential returns an OAuth credential with the given access token.
func oauthCredential(access string) *protocol.Credential {
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access}
}

// prepare sets up the fake provider so that a generate call acts out scenario s.
func prepare(t *testing.T, s conformance.Scenario) conformance.Setup {
	t.Helper()
	srv := oauthtest.New(t)
	switch s {
	case conformance.ScenarioText:
		return conformance.Setup{Account: account(srv.URL, oauthCredential(srv.IssueToken("text")))}
	case conformance.ScenarioHang:
		token, cancelled := srv.IssueHang()
		return conformance.Setup{Account: account(srv.URL, oauthCredential(token)), UpstreamCancelled: cancelled}
	case conformance.ScenarioRateLimited:
		return conformance.Setup{Account: account(srv.URL, oauthCredential(srv.IssueToken("rate_limited")))}
	case conformance.ScenarioAuthFailed:
		return conformance.Setup{Account: account(srv.URL, oauthCredential(srv.IssueToken("auth_failed")))}
	}
	t.Fatalf("no fixture for scenario %q", s)
	return conformance.Setup{}
}

// prepareLogin sets up the fake provider so that a login or refresh acts out scenario s with the
// given method.
func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := oauthtest.New(t)
	switch s {
	case conformance.LoginApproved, conformance.LoginDenied:
		approve := s == conformance.LoginApproved
		switch method {
		case "device":
			return conformance.LoginSetup{Account: account(srv.URL, nil), Act: deviceUser(srv, approve)}
		case "browser":
			srv.DenyBrowser = !approve
			return conformance.LoginSetup{Account: account(srv.URL, nil), Act: browserUser}
		case "api_key":
			key := "not-a-valid-key"
			if approve {
				key = srv.IssueToken("text")
			}
			return conformance.LoginSetup{Account: account(srv.URL, nil), Input: map[string]string{"api_key": key}}
		}
	case conformance.LoginAbandoned:
		switch method {
		case "device":
			return conformance.LoginSetup{Account: account(srv.URL, nil), UpstreamCancelled: srv.DeviceAbandoned()}
		case "browser":
			return conformance.LoginSetup{Account: account(srv.URL, nil)}
		}
	case conformance.RefreshRenewed:
		access, refresh := srv.Grant()
		return conformance.LoginSetup{Account: account(srv.URL, &protocol.Credential{
			Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh,
		})}
	case conformance.RefreshRejected:
		return conformance.LoginSetup{Account: account(srv.URL, &protocol.Credential{
			Kind: protocol.CredentialOAuth, AccessToken: "expired-access-token", RefreshToken: "refresh-token-not-issued",
		})}
	case conformance.RefreshUnavailable:
		srv.RefreshFailure = "unavailable"
		access, refresh := srv.Grant()
		return conformance.LoginSetup{Account: account(srv.URL, &protocol.Credential{
			Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh,
		})}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}

// deviceUser plays the user of a device login: it approves or denies the code the prompt shows.
func deviceUser(srv *oauthtest.Server, approve bool) func(*testing.T, protocol.Prompt) map[string]string {
	return func(t *testing.T, p protocol.Prompt) map[string]string {
		t.Helper()
		if approve {
			srv.ApproveDevice(p.UserCode)
		} else {
			srv.DenyDevice(p.UserCode)
		}
		return nil
	}
}

// browserUser plays the user of a browser login: it opens the sign-in URL without following the
// redirect, and sends back the address the browser was sent to.
func browserUser(t *testing.T, p protocol.Prompt) map[string]string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(p.URL)
	if err != nil {
		t.Fatalf("opening the sign-in URL: %v", err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		t.Fatalf("the sign-in URL answered %d without a redirect", resp.StatusCode)
	}
	return map[string]string{"redirect_url": loc}
}
