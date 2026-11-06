package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformance runs the extension conformance kit against this extension, with a fake
// OpenAI-compatible upstream that acts out each scenario.
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	up := newScenarioUpstream(t)
	conformance.Run(t, conformance.Target{
		Manifest: manifest,
		Prepare: func(t *testing.T, s conformance.Scenario) conformance.Setup {
			settings, _ := json.Marshal(map[string]string{"base_url": up.URL + "/" + string(s) + "/v1"})
			key := "sk-conformance"
			if s == conformance.ScenarioAuthFailed {
				key = "sk-revoked"
			}
			return conformance.Setup{
				Account: protocol.Account{
					ID:         "conformance",
					Settings:   settings,
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key},
				},
				UpstreamCancelled: up.cancelledFor(s),
			}
		},
	})
}

type scenarioUpstream struct {
	*httptest.Server
	mu        sync.Mutex
	cancelled map[conformance.Scenario]chan struct{}
}

func newScenarioUpstream(t *testing.T) *scenarioUpstream {
	u := &scenarioUpstream{cancelled: map[conformance.Scenario]chan struct{}{}}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.Close)
	return u
}

// cancelledFor returns a fresh channel that closes when the next hanging request is cancelled.
func (u *scenarioUpstream) cancelledFor(s conformance.Scenario) <-chan struct{} {
	if s != conformance.ScenarioHang {
		return nil
	}
	ch := make(chan struct{})
	u.mu.Lock()
	u.cancelled[s] = ch
	u.mu.Unlock()
	return ch
}

func (u *scenarioUpstream) serve(w http.ResponseWriter, r *http.Request) {
	scenario, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if rest != "v1/chat/completions" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	switch conformance.Scenario(scenario) {
	case conformance.ScenarioAuthFailed:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	case conformance.ScenarioRateLimited:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
	case conformance.ScenarioText:
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"chatcmpl-k","object":"chat.completion","created":1,"model":"kit-model",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
				conformance.ReplyText, conformance.InputTokens, conformance.OutputTokens, conformance.InputTokens+conformance.OutputTokens)
			return
		}
		streamText(w, conformance.ReplyText[:6], conformance.ReplyText[6:])
	case conformance.ScenarioHang:
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", textChunk("Hold on"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			u.mu.Lock()
			if ch := u.cancelled[conformance.ScenarioHang]; ch != nil {
				close(ch)
				delete(u.cancelled, conformance.ScenarioHang)
			}
			u.mu.Unlock()
		case <-time.After(30 * time.Second):
		}
	default:
		http.NotFound(w, r)
	}
}

func textChunk(s string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-k","object":"chat.completion.chunk","created":1,"model":"kit-model","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, s)
}

func streamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range pieces {
		fmt.Fprintf(w, "data: %s\n\n", textChunk(p))
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, `data: {"id":"chatcmpl-k","object":"chat.completion.chunk","created":1,"model":"kit-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	fmt.Fprintf(w, `data: {"id":"chatcmpl-k","object":"chat.completion.chunk","created":1,"model":"kit-model","choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`+"\n\n",
		conformance.InputTokens, conformance.OutputTokens, conformance.InputTokens+conformance.OutputTokens)
	fmt.Fprint(w, "data: [DONE]\n\n")
}
