package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with the fake Google
// login and Cloud Code acting out each scenario on an Antigravity login.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest:     manifest,
		Prepare:      prepare,
		PrepareLogin: prepareLogin,
	})
}

func signedIn(access, refresh string) *protocol.Credential {
	data, _ := json.Marshal(credentialData{ProjectID: agtest.Project})
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh, Data: data}
}

func prepare(t *testing.T, s conformance.Scenario) conformance.Setup {
	t.Helper()
	srv := agtest.New(t)
	cancelled := make(chan struct{})
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req agtest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			if r.URL.Path == "/v1internal:streamGenerateContent" {
				agtest.StreamText(w, text[:6], text[6:])
				return
			}
			agtest.JSONText(w, text)
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hold on"}]},"index":0}]}}`+"\n\n")
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
			io.WriteString(w, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
		}
	}
	access, refresh := srv.Grant()
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(access)
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(), Credential: signedIn(access, refresh)}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := agtest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings()}
	switch s {
	case conformance.LoginApproved:
		// The token exchange sends the app's own client ID and secret, and the login asked for none.
		t.Cleanup(func() {
			forms := srv.TokenForms()
			if len(forms) != 1 || forms[0].Get("grant_type") != "authorization_code" ||
				forms[0].Get("client_id") != clientID || forms[0].Get("client_secret") != clientSecret {
				t.Errorf("the token endpoint saw %v, want one authorization_code request with the app's client ID and secret", forms)
			}
		})
		return conformance.LoginSetup{Account: account, Act: browserUser}
	case conformance.LoginDenied:
		srv.DenyBrowser = true
		return conformance.LoginSetup{Account: account, Act: browserUser}
	case conformance.LoginAbandoned:
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRenewed, conformance.RefreshUnavailable:
		srv.TokenDown = s == conformance.RefreshUnavailable
		account.Credential = signedIn(srv.Grant())
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRejected:
		account.Credential = signedIn("agtest-access-expired", "agtest-refresh-not-issued")
		return conformance.LoginSetup{Account: account}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}

// browserUser plays the user of the browser login: it opens the sign-in URL without following the
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
