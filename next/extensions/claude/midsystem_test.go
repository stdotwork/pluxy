package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// System messages in the conversation move into the top-level system prompt, after it, as text
// blocks: a string becomes a block, a text block keeps its cache marker, blank text and other
// blocks are left out. Without a system message that holds text the body is not touched.
func TestMoveSystemMessages(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"moved",
			`{"model":"m","system":"Be brief.","messages":[
				{"role":"user","content":"hi"},
				{"role":"system","content":"Now answer in French."},
				{"role":"assistant","content":"ok"},
				{"role":"System","content":[{"type":"text","text":"Cite sources.","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"url","url":"https://x"}},{"type":"text","text":"  "},"Be kind."]},
				{"role":"user","content":"go"}]}`,
			`{"model":"m","system":[{"type":"text","text":"Be brief."},{"type":"text","text":"Now answer in French."},{"type":"text","text":"Cite sources.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Be kind."}],"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"go"}]}`},
		{"no system prompt", `{"messages":[{"role":"system","content":[{"type":"text","text":"Rules."}]},{"role":"user","content":"hi"}]}`,
			`{"system":[{"type":"text","text":"Rules."}],"messages":[{"role":"user","content":"hi"}]}`},
		{"no system message", `{"system":"S","messages":[{"role":"user","content":"hi"}]}`, ``},
		{"no text in it", `{"messages":[{"role":"system","content":" "},{"role":"user","content":"hi"}]}`, ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := moveSystemMessages([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if string(got) != c.in {
					t.Errorf("body changed to %s", got)
				}
				return
			}
			if !jsonEqual(got, json.RawMessage(c.want)) {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// With rebuild_mid_system_message enabled, system messages in a Messages passthrough conversation
// are moved into the top-level system prompt before RepairTurns runs. Otherwise RepairTurns sees a
// system message between an assistant tool_use and its user tool_result, fabricates an interrupted
// tool_result, and demotes the real tool_result to text; or misses a trailing assistant prefill
// followed by a system message.
func TestRebuildMidSystemBeforeRepairTurns(t *testing.T) {
	srv := claudetest.New(t)
	var cfg map[string]any
	_ = json.Unmarshal(srv.Settings(""), &cfg)
	cfg["rebuild_mid_system_message"] = true
	raw, _ := json.Marshal(cfg)
	account := protocol.Account{
		ID:         "claude",
		Settings:   raw,
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()},
	}
	ext := startExtension(t)

	t.Run("tool_use then system then tool_result", func(t *testing.T) {
		in := `{"model":"claude-opus-4-6","max_tokens":128,"system":"Base.","messages":[` +
			`{"role":"user","content":"Check the weather."},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}]},` +
			`{"role":"system","content":"Be concise."},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Sunny"}]}]}`
		req := llm.Request{
			Model:  "claude-opus-4-6",
			Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)},
		}
		evs := events(t, ext.Generate(context.Background(), account, req))
		if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
			t.Fatalf("call ended with %+v, want finish", last)
		}
		for _, ev := range evs {
			if ev.Type == llm.EventResponseStart && ev.Response != nil && ev.Response.Translation != nil && len(ev.Response.Translation.Losses) > 0 {
				t.Errorf("unexpected repair losses: %+v", ev.Response.Translation.Losses)
			}
		}
		reqs := srv.Requests()
		sent := reqs[len(reqs)-1].Body
		want := `{"model":"claude-opus-4-6","max_tokens":128,` +
			`"system":[{"type":"text","text":"Base."},{"type":"text","text":"Be concise."}],` +
			`"messages":[` +
			`{"role":"user","content":"Check the weather."},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Sunny"}]}]}`
		if !jsonEqual(sent, json.RawMessage(want)) {
			t.Errorf("sent body:\n%s\nwant:\n%s", sent, want)
		}
	})

	t.Run("trailing assistant followed by system", func(t *testing.T) {
		in := `{"model":"claude-opus-4-6","max_tokens":128,"messages":[` +
			`{"role":"user","content":"Hi"},` +
			`{"role":"assistant","content":"Sure:"},` +
			`{"role":"system","content":"Be concise."}]}`
		req := llm.Request{
			Model:  "claude-opus-4-6",
			Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)},
		}
		evs := events(t, ext.Generate(context.Background(), account, req))
		if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
			t.Fatalf("call ended with %+v, want finish", last)
		}
		reqs := srv.Requests()
		sent := reqs[len(reqs)-1].Body
		want := `{"model":"claude-opus-4-6","max_tokens":128,` +
			`"system":[{"type":"text","text":"Be concise."}],` +
			`"messages":[{"role":"user","content":"Hi"}]}`
		if !jsonEqual(sent, json.RawMessage(want)) {
			t.Errorf("sent body:\n%s\nwant:\n%s", sent, want)
		}
	})

	t.Run("disabled keeps passthrough body byte-identical", func(t *testing.T) {
		in := `{"model":"claude-opus-4-6","max_tokens":128,"messages":[` +
			`{"role":"user","content":"Check the weather."},` +
			`{"role":"system","content":"Be concise."}]}`
		req := llm.Request{
			Model:  "claude-opus-4-6",
			Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)},
		}
		got, tr, err := buildBodyFor(req, settings{CacheTTL: cacheTTL5m}, anthropicmessages.EncodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("unexpected losses: %+v", tr.Losses)
		}
		if string(got) != in {
			t.Errorf("body changed:\ngot  %s\nwant %s", got, in)
		}
	})
}
