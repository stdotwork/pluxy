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
		{"betas of mixed items", `{"betas":["b1",7,null,"",{"x":1},["b9"],true," b2 ","bad\nbeta"]}`, false, []string{"b1", "7", "true", "b2"}},
		{"not an object", `["advisor_"]`, false, nil},
		{"not JSON", `{"tools":[{"type":"advisor_x"`, false, nil},
		{"odd shapes", `{"tools":"advisor_x","system":"s \"1h\"","messages":[{"role":"user","content":"\"1h\""},{"role":"user","content":"tool_addition"}],"thinking":"block_binding"}`, false, nil},
		{"top-level 1h needs no beta", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":"hi"}]}`, false, nil},
		{"tool_addition by ref", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","name":"foo"}]}]}`, false, []string{midConversationToolChangesBeta}},
		{"tool_removal", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_removal","name":"foo"}]}]}`, false, []string{midConversationToolChangesBeta}},
		{"inline tool_addition by type", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"foo","input_schema":{}}}]}]}`, false, []string{inlineToolsBeta}},
		{"inline tool_addition by definition", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"definition":{"name":"foo"}}}]}]}`, false, []string{inlineToolsBeta}},
		{"mixed inline and removal", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_removal","name":"bar"},{"type":"tool_addition","tool":{"type":"tool_definition","name":"foo","input_schema":{}}}]}]}`, false, []string{inlineToolsBeta}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, got := bodyBetas([]byte(c.body), c.count)
			if !slices.Equal(got, c.want) {
				t.Errorf("betas = %q, want %q", got, c.want)
			}
		})
	}
}

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

func TestPostClaudeCodeHeadersOnlyFromClaudeCode(t *testing.T) {
	srv := claudetest.New(t)
	s, err := readSettings(srv.Settings(""))
	if err != nil {
		t.Fatal(err)
	}
	key := srv.IssueKey()
	claudeCode := map[string]string{"x-app": "cli", "user-agent": "claude-cli/2.1.280 (external, cli)", "anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14"}
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"Claude Code", claudeCode, true},
		{"Claude Code background", map[string]string{"x-app": "cli-bg", "user-agent": claudeCode["user-agent"], "anthropic-beta": claudeCode["anthropic-beta"]}, true},
		{"no X-App", map[string]string{"user-agent": claudeCode["user-agent"], "anthropic-beta": claudeCode["anthropic-beta"]}, false},
		{"another User-Agent", map[string]string{"x-app": "cli", "user-agent": "my-app/1.0", "anthropic-beta": claudeCode["anthropic-beta"]}, false},
		{"no claude-code beta", map[string]string{"x-app": "cli", "user-agent": claudeCode["user-agent"], "anthropic-beta": "interleaved-thinking-2025-05-14"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := map[string]string{"x-client-app": "app", "x-claude-code-agent-id": "agent-1", "x-claude-code-prompt-id": "prompt-1", "x-claude-remote-session-id": "remote-1"}
			for k, v := range c.headers {
				h[k] = v
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}},
				Request: llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Headers: h}},
			}
			n := len(srv.Requests())
			resp, err := post(context.Background(), call, s, "/v1/messages", []byte(`{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`), true, false)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			got := srv.Requests()[n].Header
			if got.Get("X-Client-App") != "app" {
				t.Errorf("X-Client-App = %q, want the client's", got.Get("X-Client-App"))
			}
			sent := got.Get("X-Claude-Code-Agent-Id") == "agent-1" && got.Get("X-Claude-Code-Prompt-Id") == "prompt-1" && got.Get("X-Claude-Remote-Session-Id") == "remote-1"
			none := got.Get("X-Claude-Code-Agent-Id") == "" && got.Get("X-Claude-Code-Prompt-Id") == "" && got.Get("X-Claude-Remote-Session-Id") == ""
			if (c.want && !sent) || (!c.want && !none) {
				t.Errorf("agent id %q, prompt id %q, remote session %q; want them sent: %v", got.Get("X-Claude-Code-Agent-Id"), got.Get("X-Claude-Code-Prompt-Id"), got.Get("X-Claude-Remote-Session-Id"), c.want)
			}
		})
	}
}

func TestPostToolChangeBetas(t *testing.T) {
	srv := claudetest.New(t)
	s, err := readSettings(srv.Settings(""))
	if err != nil {
		t.Fatal(err)
	}
	cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{"tool_addition by ref", `{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","name":"foo"}]}]}`, "mid-conversation-tool-changes-2026-07-01"},
		{"tool_removal", `{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_removal","name":"foo"}]}]}`, "mid-conversation-tool-changes-2026-07-01"},
		{"inline tool_addition", `{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"foo","input_schema":{"type":"object"}}}]}]}`, "inline-tools-2026-09-15"},
	} {
		t.Run(c.name, func(t *testing.T) {
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Credential: cred},
				Request: llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages}},
			}
			n := len(srv.Requests())
			resp, err := post(context.Background(), call, s, "/v1/messages", []byte(c.body), true, false)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := srv.Requests()[n].Header.Get("Anthropic-Beta"); got != c.want {
				t.Errorf("Anthropic-Beta = %q, want %q", got, c.want)
			}
		})
	}
}
