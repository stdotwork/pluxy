package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with a fake
// Anthropic that acts out each scenario, for an API key and for a subscription login.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	for _, kind := range []string{protocol.CredentialAPIKey, protocol.CredentialOAuth} {
		t.Run(kind, func(t *testing.T) {
			conformance.Run(t, conformance.Target{
				Manifest:     manifest,
				Prepare:      func(t *testing.T, s conformance.Scenario) conformance.Setup { return prepare(t, s, kind) },
				PrepareLogin: prepareLogin,
			})
		})
	}
}

// prepare sets up a fake Anthropic so that a generate call with a credential of kind acts out s.
func prepare(t *testing.T, s conformance.Scenario, kind string) conformance.Setup {
	t.Helper()
	srv := claudetest.New(t)
	cancelled := make(chan struct{})
	srv.Messages = func(w http.ResponseWriter, r *http.Request, req claudetest.Request) {
		var in struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(req.Body, &in)
		switch s {
		case conformance.ScenarioText:
			if in.Stream {
				claudetest.StreamText(w, conformance.ReplyText[:6], conformance.ReplyText[6:])
				return
			}
			w.Header().Set("Content-Type", "application/json")
			text, _ := json.Marshal(conformance.ReplyText)
			fmt.Fprintf(w, `{"id":"msg_k","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":%s}],`+
				`"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":%d}}`,
				text, conformance.InputTokens, conformance.OutputTokens)
		case conformance.ScenarioHang:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_k\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hold on\"}}\n\n")
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
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`)
		}
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings("")}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	switch {
	case s == conformance.ScenarioAuthFailed && kind == protocol.CredentialAPIKey:
		setup.Account.Credential = &protocol.Credential{Kind: kind, APIKey: "sk-ant-api-revoked"}
	case s == conformance.ScenarioAuthFailed:
		access, refresh := srv.Grant()
		srv.Revoke(access)
		setup.Account.Credential = &protocol.Credential{Kind: kind, AccessToken: access, RefreshToken: refresh}
	case kind == protocol.CredentialAPIKey:
		setup.Account.Credential = &protocol.Credential{Kind: kind, APIKey: srv.IssueKey()}
	default:
		access, refresh := srv.Grant()
		setup.Account.Credential = &protocol.Credential{Kind: kind, AccessToken: access, RefreshToken: refresh}
	}
	return setup
}

// prepareLogin sets up a fake Anthropic so that a login with method, or a refresh, acts out s.
func prepareLogin(t *testing.T, s conformance.LoginScenario, method string) conformance.LoginSetup {
	t.Helper()
	srv := claudetest.New(t)
	account := protocol.Account{ID: "conformance", Settings: srv.Settings("")}
	switch s {
	case conformance.LoginApproved, conformance.LoginDenied:
		approve := s == conformance.LoginApproved
		switch method {
		case "subscription":
			srv.DenySignIn = !approve
			return conformance.LoginSetup{Account: account, Act: func(t *testing.T, p protocol.Prompt) map[string]string {
				return map[string]string{"redirect_url": claudetest.SignIn(t, p.URL)}
			}}
		case "api_key":
			key := "sk-ant-api-not-issued"
			if approve {
				key = srv.IssueKey()
			}
			return conformance.LoginSetup{Account: account, Input: map[string]string{"api_key": key}}
		}
	case conformance.LoginAbandoned:
		if method == "subscription" {
			return conformance.LoginSetup{Account: account}
		}
	case conformance.RefreshRenewed, conformance.RefreshUnavailable:
		srv.TokenDown = s == conformance.RefreshUnavailable
		access, refresh := srv.Grant()
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh}
		return conformance.LoginSetup{Account: account}
	case conformance.RefreshRejected:
		account.Credential = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "claudetest-access-expired", RefreshToken: "claudetest-refresh-not-issued"}
		return conformance.LoginSetup{Account: account}
	}
	t.Fatalf("no fixture for %s with method %q", s, method)
	return conformance.LoginSetup{}
}
