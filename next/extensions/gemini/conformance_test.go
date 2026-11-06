package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The three ways an account reaches Gemini.
const (
	kindGemini    = "gemini-api-key"
	kindVertexSA  = "vertex-service-account"
	kindVertexKey = "vertex-api-key"
)

// TestConformance runs the extension conformance kit against this extension, with a fake Google
// that acts out each scenario, for each kind of account.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	for _, kind := range []string{kindGemini, kindVertexSA, kindVertexKey} {
		t.Run(kind, func(t *testing.T) {
			conformance.Run(t, conformance.Target{
				Manifest:     manifest,
				Prepare:      func(t *testing.T, s conformance.Scenario) conformance.Setup { return prepare(t, s, kind) },
				PrepareLogin: prepareLogin,
			})
		})
	}
}

// settingsFor returns account settings that point an account of kind at srv.
func settingsFor(srv *geminitest.Server, kind string) json.RawMessage {
	s := map[string]any{"base_url": srv.URL}
	if kind != kindGemini {
		s["vertex"] = map[string]any{"location": "us-central1", "project": "conformance-project"}
	}
	b, _ := json.Marshal(s)
	return b
}

// prepare sets up a fake Google so that a generate call from an account of kind acts out s.
func prepare(t *testing.T, s conformance.Scenario, kind string) conformance.Setup {
	t.Helper()
	srv := geminitest.New(t)
	cancelled := make(chan struct{})
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		switch s {
		case conformance.ScenarioText:
			text := conformance.ReplyText
			if req.Stream {
				geminitest.StreamText(w, text[:6], text[6:])
				return
			}
			geminitest.JSONText(w, text)
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hold on"}]},"index":0}]}`+"\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-time.After(30 * time.Second):
			}
		case conformance.ScenarioRateLimited:
			geminitest.RateLimited(w, 20)
		}
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: settingsFor(srv, kind)}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	failed := s == conformance.ScenarioAuthFailed
	switch kind {
	case kindVertexSA:
		setup.Account.Credential = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("conformance-project", !failed)}
	default:
		key := srv.IssueKey()
		if failed {
			srv.Revoke(key)
		}
		setup.Account.Credential = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
	}
	return setup
}

// prepareLogin sets up a fake Google so that a login with method acts out s. Both methods are
// key logins: approved with a key Google accepts, denied with one it refuses.
func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := geminitest.New(t)
	approve := s == conformance.LoginApproved
	if s != conformance.LoginApproved && s != conformance.LoginDenied {
		t.Fatalf("no fixture for %s with method %q", s, method)
	}
	switch method {
	case "api_key":
		key := srv.IssueKey()
		if !approve {
			srv.Revoke(key)
		}
		return conformance.LoginSetup{Account: protocol.Account{ID: "conformance", Settings: settingsFor(srv, kindGemini)},
			Input: map[string]string{"api_key": key}}
	case "service_account":
		return conformance.LoginSetup{Account: protocol.Account{ID: "conformance", Settings: settingsFor(srv, kindVertexSA)},
			Input: map[string]string{"service_account": string(srv.ServiceAccount("conformance-project", approve))}}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}
