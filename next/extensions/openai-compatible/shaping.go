package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Values of the account setting developer_role.
const (
	roleDeveloper = "developer"
	roleSystem    = "system"
)

// shaping is how an account's Chat requests are shaped for its upstream.
type shaping struct {
	// developerRole is the role a developer message is sent as: roleDeveloper or roleSystem.
	developerRole string
	// stringContent sends a message whose content is all text parts as one string.
	stringContent bool
	// passback is nil to leave reasoning_content as the client sent it, true to pass the reasoning
	// of earlier assistant turns back, and false to send none.
	passback *bool
}

// preset is the shaping a vendor's own documentation asks for; a field left unset is not one.
type preset struct {
	developerRole string
	stringContent *bool
	passback      *bool
}

func ptr(b bool) *bool { return &b }

// presets are the vendors whose API documentation says a Chat request needs shaping, by base URL
// host. A host not listed gets none, and an account setting always wins over a preset.
//
//   - DeepSeek: its messages are system, user, assistant and tool (no developer role), a system or
//     assistant message's content is a string, and thinking mode with tools needs the reasoning
//     of earlier turns passed back (https://api-docs.deepseek.com/api/create-chat-completion and
//     https://api-docs.deepseek.com/guides/thinking_mode).
//   - Mistral: its messages are system, user, assistant and tool (no developer role), and an
//     assistant message takes no field other than role, content, tool_calls and prefix, so no
//     reasoning_content (the SystemMessage and AssistantMessage schemas of
//     https://docs.mistral.ai/openapi.yaml, both "additionalProperties: false").
//
// OpenRouter and Groq have none: their documentation names no role, content or field they need
// shaped. Groq says messages[].name is refused, which no setting here covers.
var presets = map[string]preset{
	"api.deepseek.com": {developerRole: roleSystem, stringContent: ptr(true), passback: ptr(true)},
	"api.mistral.ai":   {developerRole: roleSystem, passback: ptr(false)},
}

// tierFields say which of a request's service tier, verbosity, web search, code execution tools,
// structured output, candidate count and replayed assistant annotations an upstream's API takes.
type tierFields struct {
	serviceTier, verbosity, webSearch, codeExecution, structuredOutput, candidateCount, assistantAnnotations bool
}

// tierHosts are the vendors whose API documentation names a service tier, a verbosity, a hosted
// web search tool, or a hosted code execution tool in the request, by base URL host and then by
// the format of the wire. A request from another format carries the client's tier, verbosity or
// translated hosted tools only to these; any other host gets none of them, since an upstream may
// refuse a field or tool it does not list (Mistral's Chat request does, with
// "additionalProperties: false", and the Codex CLI sends a verbosity of its own). A client's own
// body goes upstream as it came whatever the host. Mistral documents a service_tier of its own,
// whose values are not checked here, so it is not listed.
//
//   - api.openai.com: service_tier and verbosity in the Chat Completions create reference
//     (https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
//     and service_tier and text.verbosity in the Responses create reference
//     (https://developers.openai.com/api/reference/resources/responses/methods/create); hosted web
//     search in OpenAI's web search guide (https://developers.openai.com/api/docs/guides/tools-web-search)
//     — Responses takes web_search (with filters, user_location and search_context_size), and Chat
//     Completions takes web_search_options, which the Chat encoder further restricts to specialized
//     search models (such as gpt-5-search-api) without function tools; hosted code execution
//     (code_interpreter) in https://developers.openai.com/api/docs/guides/tools-code-interpreter
//     for Responses only (Chat Completions has no code execution tool), gated by the Responses
//     encoder to documented models.
//   - api.anthropic.com: service_tier in the Messages create reference
//     (https://platform.claude.com/docs/en/api/messages/create), described at
//     https://platform.claude.com/docs/en/api/service-tiers. Its fast mode needs a beta header this
//     extension does not add, so the priority tier is not sent. Messages has no verbosity. Hosted
//     web search (web_search_20250305) is documented at
//     https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool, and hosted
//     code execution (code_execution_20250825, requiring no beta header) at
//     https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool; the
//     Messages encoder gates newly translated tools to the documented Claude model families while
//     keeping the existing Responses web_search mapping ungated.
//   - generativelanguage.googleapis.com: serviceTier in GenerateContentRequest
//     (https://ai.google.dev/api/generate-content/generate-content), googleSearch in
//     https://ai.google.dev/gemini-api/docs/google-search and codeExecution in
//     https://aistudio.google.com/docs/code-execution (gated by the Gemini encoder to documented
//     model families without function declarations). GenerateContent has no verbosity.
//   - openrouter.ai: replayed assistant message annotations on Chat Completions
//     (https://openrouter.ai/docs/features/multimodal/pdfs).
var tierHosts = map[string]map[string]tierFields{
	"api.openai.com": {
		llm.FormatOpenAIChat:      {serviceTier: true, verbosity: true, webSearch: true, candidateCount: true},
		llm.FormatOpenAIResponses: {serviceTier: true, verbosity: true, webSearch: true, codeExecution: true},
	},
	"api.anthropic.com":                 {llm.FormatAnthropicMessages: {serviceTier: true, webSearch: true, codeExecution: true, structuredOutput: true}},
	"generativelanguage.googleapis.com": {llm.FormatGeminiGenerate: {serviceTier: true, webSearch: true, codeExecution: true, candidateCount: true}},
	"openrouter.ai":                     {llm.FormatOpenAIChat: {assistantAnnotations: true}},
}

// tierFieldsOf returns which of a tier and a verbosity the account's upstream takes on the wire of
// format.
func tierFieldsOf(s settings, format string) tierFields {
	u, err := url.Parse(s.BaseURL)
	if err != nil {
		return tierFields{}
	}
	return tierHosts[strings.ToLower(u.Hostname())][format]
}

// shapingOf returns the shaping for an account: its own settings where set, else its host's preset.
func shapingOf(s settings) shaping {
	var p preset
	if u, err := url.Parse(s.BaseURL); err == nil {
		p = presets[strings.ToLower(u.Hostname())]
	}
	sh := shaping{developerRole: roleDeveloper, passback: p.passback}
	if p.developerRole != "" {
		sh.developerRole = p.developerRole
	}
	if s.DeveloperRole != "" {
		sh.developerRole = s.DeveloperRole
	}
	sh.stringContent = p.stringContent != nil && *p.stringContent
	if s.StringContent != nil {
		sh.stringContent = *s.StringContent
	}
	if s.ReasoningPassback != nil {
		sh.passback = s.ReasoningPassback
	}
	return sh
}

// checkShaping returns the reason an account's shaping settings are refused, or "".
func checkShaping(s settings) string {
	switch s.DeveloperRole {
	case "", roleDeveloper, roleSystem:
		return ""
	}
	return fmt.Sprintf("account setting developer_role must be %q or %q", roleDeveloper, roleSystem)
}

// shapeChat shapes the body of a Chat request for the account's upstream, whether the client's own
// or a translation: the developer role, string content, reasoning_content passback (see shaping)
// and stripping replayed assistant annotations on hosts not documented to accept them. It returns
// body itself when nothing changes, and the losses it causes.
func shapeChat(body []byte, s settings) ([]byte, []llm.Loss, error) {
	sh := shapingOf(s)
	stripAnnotations := !tierFieldsOf(s, llm.FormatOpenAIChat).assistantAnnotations && bytes.Contains(body, []byte(`"annotations"`))
	if sh.developerRole == roleDeveloper && !sh.stringContent && sh.passback == nil && !stripAnnotations {
		return body, nil, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return body, nil, nil
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(top["messages"], &messages) != nil {
		return body, nil, nil
	}
	// Reasoning is passed back only where DeepSeek takes it: with tools, and with thinking not
	// turned off. Without tools the API ignores it.
	fill := sh.passback != nil && *sh.passback && hasTools(top) && !thinkingOff(top)
	var losses []llm.Loss
	changed := false
	for i, m := range messages {
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role == roleDeveloper && sh.developerRole == roleSystem {
			m["role"] = json.RawMessage(`"system"`)
			changed = true
		}
		if sh.stringContent {
			if text, ok := allText(m["content"]); ok {
				m["content"], _ = json.Marshal(text)
				changed = true
			}
		}
		if stripAnnotations && role == "assistant" {
			if _, ok := m["annotations"]; ok {
				delete(m, "annotations")
				changed = true
				losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossApproximated, Path: fmt.Sprintf("messages[%d].annotations", i),
					Detail: "Chat Completions assistant messages do not take annotations"})
			}
		}
		if sh.passback == nil || role != "assistant" {
			continue
		}
		switch {
		case !*sh.passback:
			if _, ok := m["reasoning_content"]; ok {
				delete(m, "reasoning_content")
				changed = true
			}
		case fill && !hasText(m["reasoning_content"]):
			// DeepSeek answers 400 to an earlier assistant turn without reasoning_content. One
			// space is the least it takes; the model gets a blank reasoning chain for the turn.
			m["reasoning_content"] = json.RawMessage(`" "`)
			changed = true
			losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossApproximated, Path: fmt.Sprintf("messages[%d].reasoning_content", i),
				Detail: "no reasoning was kept for this turn; a blank one was sent, as the upstream refuses a turn without it"})
		}
	}
	if !changed {
		return body, nil, nil
	}
	var err error
	if top["messages"], err = json.Marshal(messages); err != nil {
		return nil, nil, err
	}
	out, err := json.Marshal(top)
	return out, losses, err
}

// hasTools reports whether the request declares tools.
func hasTools(top map[string]json.RawMessage) bool {
	var tools []json.RawMessage
	return json.Unmarshal(top["tools"], &tools) == nil && len(tools) > 0
}

// thinkingOff reports whether the request turns thinking off (DeepSeek's thinking.type).
func thinkingOff(top map[string]json.RawMessage) bool {
	var t struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(top["thinking"], &t) == nil && t.Type == "disabled"
}

// hasText reports whether raw is a string with something in it.
func hasText(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s != ""
}

// allText returns the text of content that is a non-empty array of text parts and nothing else.
func allText(raw json.RawMessage) (string, bool) {
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" || p.Text == nil {
			return "", false
		}
		b.WriteString(*p.Text)
	}
	return b.String(), true
}
