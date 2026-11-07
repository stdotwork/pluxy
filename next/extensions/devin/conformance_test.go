package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with the fake Devin
// acting out each scenario.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest:     manifest,
		Prepare:      prepare,
		PrepareLogin: prepareLogin,
	})
}

func tokenCredential(tok string) *protocol.Credential {
	return &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tok}
}

func prepare(t *testing.T, s conformance.Scenario) conformance.Setup {
	t.Helper()
	srv := devintest.New(t)
	cancelled := make(chan struct{})
	srv.Chat = func(w http.ResponseWriter, r *http.Request, req devintest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			devintest.Reply(w, devintest.Text(text[:6]), devintest.Text(text[6:]), devintest.Usage(11, 7), devintest.End(""))
		case conformance.ScenarioHang:
			devintest.Reply(w, devintest.Text("Hold on"))
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
			io.WriteString(w, `{"code":"resource_exhausted","message":"Too many requests."}`)
		}
	}
	tok := srv.Token()
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(tok)
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(), Credential: tokenCredential(tok)}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}

func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := devintest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings()}
	switch {
	case method == "token" && s == conformance.LoginApproved:
		return conformance.LoginSetup{Account: account, Input: map[string]string{"session_token": srv.Token()}}
	case method == "token" && s == conformance.LoginDenied:
		return conformance.LoginSetup{Account: account, Input: map[string]string{"session_token": "devin-session-token$not-issued"}}
	case method == "browser" && s == conformance.LoginApproved:
		return conformance.LoginSetup{Account: account, Act: browserUser}
	case method == "browser" && s == conformance.LoginDenied:
		srv.DenyBrowser = true
		return conformance.LoginSetup{Account: account, Act: browserUser}
	case method == "browser" && s == conformance.LoginAbandoned:
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
	return map[string]string{"code": loc}
}
