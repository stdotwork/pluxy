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
// acts out each scenario. The extension has no login methods: its accounts come from an API key
// in the config.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{Manifest: manifest, Prepare: prepare})
}

func prepare(t *testing.T, s conformance.Scenario) conformance.Setup {
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
	key := srv.Key()
	if s == conformance.ScenarioAuthFailed {
		srv.Revoke(key)
	}
	setup := conformance.Setup{Account: protocol.Account{ID: "conformance", Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}}
	if s == conformance.ScenarioHang {
		setup.UpstreamCancelled = cancelled
	}
	return setup
}
