package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestCompactEndpoint(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	for _, kind := range []string{protocol.CredentialAPIKey, protocol.CredentialOAuth} {
		t.Run(kind, func(t *testing.T) {
			const reply = `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":20,"output_tokens":3}}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses/compact" || r.Method != "POST" {
					t.Errorf("target = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer local-fixture" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("headers = %v", r.Header)
				}
				if r.Header.Get("X-XAI-Token-Auth") != "" || r.Header.Get("User-Agent") != "pluxy-xai/"+version {
					t.Errorf("compact headers = %v", r.Header)
				}
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				sameJSON(t, "upstream compaction request", b, `{"model":"grok-4.6","input":[{"role":"user","content":"hi"}],"previous_response_id":"resp_previous"}`)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, reply)
			}))
			defer srv.Close()
			settings, _ := json.Marshal(map[string]string{"base_url": srv.URL + "/v1"})
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "local-fixture"}
			if kind == protocol.CredentialOAuth {
				cred = &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "local-fixture"}
			}
			req := llm.Request{Model: "grok-4.6", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(`{"model":"alias","stream":false,"input":[{"type":"compaction_trigger"},{"role":"user","content":"hi"}],"previous_response_id":"resp_previous","tools":[{"type":"x_search"}],"tool_choice":{"type":"x_search"},"temperature":0.5,"top_p":0.9,"top_k":2,"stop":["x"],"max_output_tokens":100}`)}}
			s := ext.Endpoint(t.Context(), protocol.Account{ID: "xai", Settings: settings, Credential: cred}, req)
			defer s.Close()
			var body strings.Builder
			for {
				ev := s.Next()
				if ev.Type == llm.EventError {
					t.Fatalf("endpoint error: %+v", ev.Error)
				}
				if ev.Type == llm.EventNative {
					var chunk string
					if err := json.Unmarshal(ev.Raw, &chunk); err != nil {
						t.Fatal(err)
					}
					body.WriteString(chunk)
				}
				if ev.Terminal() {
					usage := ev.Usage
					if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 20 || usage.OutputTokens == nil || *usage.OutputTokens != 3 {
						t.Errorf("usage = %+v", usage)
					}
					break
				}
			}
			if body.String() != reply {
				t.Errorf("reply = %s", body.String())
			}
		})
	}
}

func TestCompactBaseURL(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{defaultBaseURL, defaultBaseURL},
		{cliChatBaseURL, defaultBaseURL},
		{cliChatBaseURL + "/", defaultBaseURL},
		{"https://custom.example/v1", "https://custom.example/v1"},
	} {
		if got := compactBaseURL(tc.base); got != tc.want {
			t.Errorf("compact base %q = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestCompactErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		reply       string
		code, scope string
		retry       bool
	}{
		{"endpoint missing", 404, `{"detail":"Not Found"}`, llm.CodeNotFound, llm.ScopeRequest, false},
		{"model missing", 404, `{"error":{"message":"model grok-4.6 not found"}}`, llm.CodeNotFound, llm.ScopeModel, true},
		{"bad credential", 401, `{"error":"invalid key"}`, llm.CodeAuth, llm.ScopeCredential, true},
		{"bad JSON", 200, `not JSON`, llm.CodeUpstream, llm.ScopeEndpoint, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.reply) }))
			defer srv.Close()
			set, _ := json.Marshal(map[string]string{"base_url": srv.URL})
			err := endpoint(t.Context(), sdk.Call{Account: protocol.Account{Settings: set, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "local-fixture"}}, Request: llm.Request{Model: "grok-4.6", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(`{"model":"grok-4.6","input":[]}`)}}}, nil)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != tc.code || e.Scope != tc.scope || e.Retryable != tc.retry {
				t.Fatalf("error = %+v", err)
			}
		})
	}
}
