package main

import (
	"encoding/json"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// chatRequest reads a Chat Completions body as core does for a Chat client's request.
func chatRequest(t *testing.T, body string) llm.Request {
	t.Helper()
	req, err := openaichat.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// shaped builds the upstream body for a Chat client's request to a Chat account with settings, and
// returns its messages (as generic JSON), its top-level fields and the translation report.
func shaped(t *testing.T, settingsJSON, body string) (messages []map[string]any, top map[string]any, tr *llm.Translation) {
	t.Helper()
	var s settings
	if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
		t.Fatal(err)
	}
	out, tr, err := upstreamBody(chatRequest(t, body), llm.FormatOpenAIChat, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("body %s: %v", out, err)
	}
	raw, _ := json.Marshal(top["messages"])
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	return messages, top, tr
}

const developerRequest = `{"model":"m","messages":[
	{"role":"developer","content":"Be brief."},
	{"role":"user","content":"hi"}]}`

// developer_role "system" sends a developer message as a system one, to a client's own Chat body
// and to one translated from Responses; the default sends it as it came.
func TestDeveloperRole(t *testing.T) {
	for _, c := range []struct{ settings, want string }{
		{`{"base_url":"https://example.test/v1"}`, "developer"},
		{`{"base_url":"https://example.test/v1","developer_role":"developer"}`, "developer"},
		{`{"base_url":"https://example.test/v1","developer_role":"system"}`, "system"},
	} {
		msgs, _, _ := shaped(t, c.settings, developerRequest)
		if msgs[0]["role"] != c.want || msgs[1]["role"] != "user" {
			t.Errorf("%s: roles %v %v, want %s user", c.settings, msgs[0]["role"], msgs[1]["role"], c.want)
		}
	}
	// From a Responses client: the encoder writes a developer message, and the setting changes it.
	responses, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":[{"type":"message","role":"developer","content":"Be brief."},{"type":"message","role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := upstreamBody(responses, llm.FormatOpenAIChat, settings{DeveloperRole: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"role":"system"`) || strings.Contains(string(out), `"developer"`) {
		t.Errorf("translated body = %s, want the developer message sent as system", out)
	}
}

const partsRequest = `{"model":"m","messages":[
	{"role":"system","content":[{"type":"text","text":"You are "},{"type":"text","text":"terse."}]},
	{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]},
	{"role":"assistant","content":[{"type":"text","text":"ok"}]},
	{"role":"user","content":"plain"}]}`

// string_content sends content that is all text parts as one string, and leaves every other
// message as it is: a message with an image keeps its parts.
func TestStringContent(t *testing.T) {
	msgs, _, _ := shaped(t, `{"base_url":"https://example.test/v1","string_content":true}`, partsRequest)
	if got := msgs[0]["content"]; got != "You are terse." {
		t.Errorf("system content = %#v, want one string", got)
	}
	if _, ok := msgs[1]["content"].([]any); !ok {
		t.Errorf("image message content = %#v, want its parts kept", msgs[1]["content"])
	}
	if got := msgs[2]["content"]; got != "ok" {
		t.Errorf("assistant content = %#v, want one string", got)
	}
	if got := msgs[3]["content"]; got != "plain" {
		t.Errorf("string content = %#v, want it unchanged", got)
	}
	// Unset or false: the parts are sent as they came.
	for _, set := range []string{`{"base_url":"https://example.test/v1"}`, `{"base_url":"https://example.test/v1","string_content":false}`} {
		msgs, _, _ := shaped(t, set, partsRequest)
		if _, ok := msgs[0]["content"].([]any); !ok {
			t.Errorf("%s: system content = %#v, want parts", set, msgs[0]["content"])
		}
	}
}

// A reasoning effort the account does not take is reported as an approximated loss naming the
// effort asked and the one sent, also when the client's own body is otherwise sent unchanged.
func TestEffortClampIsReported(t *testing.T) {
	const noEffort = `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct {
		name, body, suffix, asked, sent string
	}{
		{"xhigh", `{"model":"m","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`, "", "xhigh", "high"},
		{"max", `{"model":"m","reasoning_effort":"max","messages":[{"role":"user","content":"hi"}]}`, "", "max", "high"},
		{"suffix", noEffort, "xhigh", "xhigh", "high"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := chatRequest(t, c.body)
			req.Options.ThinkingSuffix = c.suffix
			_, tr, err := upstreamBody(req, llm.FormatOpenAIChat, settings{})
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, l := range tr.Losses {
				found = found || (l.Kind == llm.LossApproximated && strings.Contains(l.Detail, `"`+c.asked+`"`) && strings.Contains(l.Detail, `"`+c.sent+`"`))
			}
			if !found {
				t.Fatalf("losses = %+v, want an approximated one naming %q asked and %q sent", tr.Losses, c.asked, c.sent)
			}
			if tr.Passthrough {
				t.Errorf("translation says the client's body was sent unchanged: %+v", tr)
			}
		})
	}
	// An effort the account takes loses nothing and the body is passed through.
	_, tr, err := upstreamBody(chatRequest(t, `{"model":"m","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), llm.FormatOpenAIChat, settings{})
	if err != nil || len(tr.Losses) != 0 || !tr.Passthrough {
		t.Errorf("high: err %v, translation %+v, want a passthrough with no losses", err, tr)
	}
	// A level the account lists is sent as it is.
	_, tr, err = upstreamBody(chatRequest(t, `{"model":"m","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`), llm.FormatOpenAIChat, settings{ReasoningLevels: []string{"low", "xhigh"}})
	if err != nil || len(tr.Losses) != 0 {
		t.Errorf("xhigh with xhigh listed: err %v, losses %+v, want none", err, tr.Losses)
	}
}

const toolTurnRequest = `{"model":"deepseek-flash","tools":[{"type":"function","function":{"name":"get_date","parameters":{"type":"object"}}}],"messages":[
	{"role":"user","content":"date?"},
	{"role":"assistant","content":"","reasoning_content":"I need the date.","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_date","arguments":"{}"}}]},
	{"role":"tool","tool_call_id":"c1","content":"2026-11-20"},
	{"role":"assistant","content":"It is 2026-11-20."},
	{"role":"user","content":"thanks"}]}`

func reasoningOf(m map[string]any) any { return m["reasoning_content"] }

// On DeepSeek a request with tools carries the reasoning_content of every earlier assistant
// message (without it the API answers 400), and one that lost it gets a blank placeholder. The
// base URL host selects it; reasoning_passback does for any other host.
func TestReasoningPassbackWithTools(t *testing.T) {
	for _, c := range []struct {
		name, settings string
		on             bool
		off            bool // reasoning_passback false: nothing is sent back, not even what the client sent
	}{
		{"deepseek host", `{"base_url":"https://api.deepseek.com"}`, true, false},
		{"deepseek host with path", `{"base_url":"https://api.deepseek.com/v1/"}`, true, false},
		{"setting on another host", `{"base_url":"https://example.test/v1","reasoning_passback":true}`, true, false},
		{"another host", `{"base_url":"https://example.test/v1"}`, false, false},
		{"deepseek host, setting off", `{"base_url":"https://api.deepseek.com","reasoning_passback":false}`, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			msgs, _, tr := shaped(t, c.settings, toolTurnRequest)
			if got, want := reasoningOf(msgs[1]), any("I need the date."); c.off {
				want = nil
				if got != want {
					t.Errorf("tool-call turn reasoning_content = %#v, want none", got)
				}
			} else if got != want {
				t.Errorf("tool-call turn reasoning_content = %#v, want it kept", got)
			}
			filled := reasoningOf(msgs[3]) == " "
			if filled != c.on {
				t.Errorf("answer turn reasoning_content = %#v, placeholder sent %v, want %v", reasoningOf(msgs[3]), filled, c.on)
			}
			if c.on != (len(tr.Losses) > 0) {
				t.Errorf("losses = %+v, want one only when a placeholder was sent", tr.Losses)
			}
		})
	}
}

// Without tools DeepSeek ignores reasoning_content, and with thinking off it has none to pass
// back: nothing is added, and what the client sent stays.
func TestReasoningPassbackNotNeeded(t *testing.T) {
	noTools := strings.Replace(toolTurnRequest, `"tools":[{"type":"function","function":{"name":"get_date","parameters":{"type":"object"}}}],`, "", 1)
	off := strings.Replace(toolTurnRequest, `"messages"`, `"thinking":{"type":"disabled"},"messages"`, 1)
	for name, body := range map[string]string{"no tools": noTools, "thinking disabled": off} {
		msgs, _, tr := shaped(t, `{"base_url":"https://api.deepseek.com"}`, body)
		if got := reasoningOf(msgs[3]); got != nil {
			t.Errorf("%s: answer turn reasoning_content = %#v, want none added", name, got)
		}
		if got := reasoningOf(msgs[1]); got != "I need the date." {
			t.Errorf("%s: tool-call turn reasoning_content = %#v, want it kept", name, got)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("%s: losses %+v, want none", name, tr.Losses)
		}
	}
}

// reasoning_passback false takes reasoning_content off every message, for a strict upstream.
func TestReasoningPassbackOff(t *testing.T) {
	msgs, _, _ := shaped(t, `{"base_url":"https://example.test/v1","reasoning_passback":false}`, toolTurnRequest)
	for i, m := range msgs {
		if _, ok := m["reasoning_content"]; ok {
			t.Errorf("message %d still has reasoning_content: %v", i, m)
		}
	}
}

// A reply's reasoning that comes back in a Claude or Responses request is passed back too: the
// translated request has it on its assistant message, and DeepSeek keeps it there.
func TestReasoningPassbackFromTranslatedHistory(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"m","tools":[{"type":"function","name":"get_date","parameters":{"type":"object"}}],"input":[
		{"type":"message","role":"user","content":"date?"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"I need the date."}]},
		{"type":"function_call","call_id":"c1","name":"get_date","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"2026-11-20"},
		{"type":"message","role":"assistant","content":"Done."},
		{"type":"message","role":"user","content":"thanks"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := upstreamBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://api.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, m := range got.Messages {
		if m["role"] == "assistant" && m["reasoning_content"] == nil {
			t.Errorf("assistant message without reasoning_content: %v", m)
		}
	}
	if got.Messages[1]["reasoning_content"] != "I need the date." {
		t.Errorf("messages = %s, want the reasoning kept on the tool-call turn", out)
	}
}

// A vendor preset applies where the account sets nothing, and the account's own value wins.
func TestPresets(t *testing.T) {
	// DeepSeek: system role (its messages take no developer role) and string content.
	msgs, _, _ := shaped(t, `{"base_url":"https://api.deepseek.com"}`, strings.Replace(developerRequest, `"Be brief."`, `[{"type":"text","text":"Be brief."}]`, 1))
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "Be brief." {
		t.Errorf("DeepSeek: first message %v, want a system message with string content", msgs[0])
	}
	msgs, _, _ = shaped(t, `{"base_url":"https://api.deepseek.com","developer_role":"developer","string_content":false}`, strings.Replace(developerRequest, `"Be brief."`, `[{"type":"text","text":"Be brief."}]`, 1))
	if msgs[0]["role"] != "developer" {
		t.Errorf("DeepSeek with developer_role set: role %v, want the account's value", msgs[0]["role"])
	}
	if _, ok := msgs[0]["content"].([]any); !ok {
		t.Errorf("DeepSeek with string_content false: content %#v, want parts", msgs[0]["content"])
	}
	// Mistral: system role, and no reasoning_content (its assistant message takes no other fields).
	msgs, _, _ = shaped(t, `{"base_url":"https://api.mistral.ai/v1"}`, toolTurnRequest)
	if _, ok := msgs[1]["reasoning_content"]; ok {
		t.Errorf("Mistral: assistant message %v, want no reasoning_content", msgs[1])
	}
	msgs, _, _ = shaped(t, `{"base_url":"https://api.mistral.ai/v1"}`, developerRequest)
	if msgs[0]["role"] != "system" {
		t.Errorf("Mistral: role %v, want system", msgs[0]["role"])
	}
	// Hosts with no preset are untouched: Groq and OpenRouter, whose docs name no such need.
	for _, host := range []string{"https://api.groq.com/openai/v1", "https://openrouter.ai/api/v1", "https://api.openai.com/v1"} {
		msgs, _, _ := shaped(t, `{"base_url":"`+host+`"}`, developerRequest)
		if msgs[0]["role"] != "developer" {
			t.Errorf("%s: role %v, want developer", host, msgs[0]["role"])
		}
	}
}

// The shaping settings apply to a Chat upstream only; another wire is sent as before.
func TestShapingIsChatOnly(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(developerRequest))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := upstreamBody(req, llm.FormatOpenAIResponses, settings{DeveloperRole: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"system"`) {
		t.Errorf("Responses body = %s, want the developer role as the encoder wrote it", out)
	}
}

// A shaping setting with a value it does not know is refused, as an unknown wire_api is.
func TestShapingSettingsAreChecked(t *testing.T) {
	call := func(s string) error {
		_, _, _, err := account(callWith(s))
		return err
	}
	if err := call(`{"base_url":"https://example.test/v1","developer_role":"system","string_content":true,"reasoning_passback":false}`); err != nil {
		t.Errorf("valid settings refused: %v", err)
	}
	if err := call(`{"base_url":"https://example.test/v1","developer_role":"admin"}`); err == nil || !strings.Contains(err.Error(), "developer_role") {
		t.Errorf("developer_role admin: err = %v, want it refused", err)
	}
}

func callWith(settingsJSON string) sdk.Call {
	return sdk.Call{Account: protocol.Account{ID: "o", Settings: json.RawMessage(settingsJSON),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"}}}
}
