package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Each body feature that needs a beta gets it, in today's core's order; a body that needs none
// gets none and is left as it is.
func TestBodyBetas(t *testing.T) {
	for _, c := range []struct {
		name  string
		body  string
		count bool
		want  []string
	}{
		{"plain", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, false, nil},
		{"advisor tool", `{"tools":[{"name":"x","input_schema":{}},{"type":" Advisor_20260301","name":"advisor"}]}`, false, []string{advisorBeta}},
		{"1h on a tool", `{"tools":[{"name":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, false, []string{extendedCacheTTLBeta}},
		{"1h in system", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, false, []string{extendedCacheTTLBeta}},
		{"1h in a message", `{"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, false, []string{extendedCacheTTLBeta}},
		{"1h on a count", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, true, nil},
		{"5m only", `{"system":[{"type":"text","text":"1h","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`, false, nil},
		{"block binding", `{"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`, false, []string{thinkingBindingBeta}},
		{"all, in order", `{"betas":["b1","b2"],"thinking":{"type":"adaptive","block_binding":{}},"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"type":"advisor_20260301","name":"advisor"}]}`, false,
			[]string{advisorBeta, "b1", "b2", extendedCacheTTLBeta, thinkingBindingBeta}},
		{"betas as one string", `{"betas":"b1","messages":[]}`, false, []string{"b1"}},
		{"not an object", `["advisor_"]`, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, got := bodyBetas([]byte(c.body), c.count)
			if !slices.Equal(got, c.want) {
				t.Errorf("betas = %q, want %q", got, c.want)
			}
		})
	}
}

// A body's betas field is removed; the rest of the body keeps its values byte for byte, HTML
// characters in strings included. A body without the field is not touched.
func TestBodyBetasRemovesBetasField(t *testing.T) {
	in := `{"model":"m","betas":["b1"],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"a <b> & c","signature":"Es+ig/="}]}]}`
	out, _ := bodyBetas([]byte(in), false)
	if strings.Contains(string(out), `"betas"`) || !strings.Contains(string(out), `"thinking":"a <b> & c","signature":"Es+ig/="`) {
		t.Errorf("body = %s, want no betas field and the rest unchanged", out)
	}
	var v map[string]any
	if json.Unmarshal(out, &v) != nil || len(v) != 2 {
		t.Errorf("body = %s, want valid JSON with model and messages", out)
	}
	plain := `{"model":"m",  "system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	if out, _ := bodyBetas([]byte(plain), false); string(out) != plain {
		t.Errorf("body = %s, want it unchanged", out)
	}
}

// The betas a body needs go after the client's own (and after the OAuth beta of a subscription),
// without repeating one the client already sent, and the body's betas field never reaches the
// upstream.
func TestPostAddsBodyBetas(t *testing.T) {
	srv := claudetest.New(t)
	s, err := readSettings(srv.Settings(""))
	if err != nil {
		t.Fatal(err)
	}
	access, refresh := srv.Grant()
	for _, c := range []struct {
		name string
		cred *protocol.Credential
		want string
	}{
		{"api key", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
			"interleaved-thinking-2025-05-14,advisor-tool-2026-03-01,b1,thinking-binding-controls-2026-08-01"},
		{"subscription", &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh},
			"interleaved-thinking-2025-05-14,advisor-tool-2026-03-01,oauth-2025-04-20,b1,thinking-binding-controls-2026-08-01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Credential: c.cred},
				Request: llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages,
					Headers: map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,advisor-tool-2026-03-01"}}},
			}
			body := `{"model":"claude-test","max_tokens":5,"betas":["b1"],"thinking":{"type":"adaptive","block_binding":{}},` +
				`"tools":[{"type":"advisor_20260301","name":"advisor"}],"messages":[{"role":"user","content":"hi"}]}`
			n := len(srv.Requests())
			resp, err := post(context.Background(), call, s, "/v1/messages", []byte(body), true, false)
			if err != nil {
				t.Fatalf("the fake refused the request: %v", err)
			}
			resp.Body.Close()
			got := srv.Requests()[n]
			if v := got.Header.Get("Anthropic-Beta"); v != c.want {
				t.Errorf("Anthropic-Beta = %q, want %q", v, c.want)
			}
			if strings.Contains(string(got.Body), `"betas"`) {
				t.Errorf("upstream body %s still has the betas field", got.Body)
			}
		})
	}
}
