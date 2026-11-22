package openairesponses

import (
	"encoding/json"
	"strings"
)

// codeInterpreterModels holds the OpenAI model IDs whose official model pages under
// https://developers.openai.com/api/docs/models/ list code_interpreter under "Supported tools"
// for the Responses API and whose endpoint table supports Responses. Dated snapshots (<id>-YYYY-MM-DD) match their base model ID; models
// that do not list code_interpreter (such as gpt-5.4-pro, gpt-5.2-pro, gpt-5*-codex, gpt-5-pro,
// o1, o1-pro, o3-pro, gpt-4o-search-preview, audio/realtime/image/embedding models) are absent.
//
// shortcut: decided from a name list of documented models, since protocol v0 has no per-model
// code execution capability. Move this to per-model capabilities when describe carries them.
var codeInterpreterModels = map[string]struct{}{
	"chat-latest":              {},
	"gpt-4.1":                  {},
	"gpt-4.1-mini":             {},
	"gpt-4.1-nano":             {},
	"gpt-4o":                   {},
	"gpt-4o-mini":              {},
	"gpt-5":                    {},
	"gpt-5-chat-latest":        {},
	"gpt-5-mini":               {},
	"gpt-5-nano":               {},
	"gpt-5.1":                  {},
	"gpt-5.1-chat-latest":      {},
	"gpt-5.2":                  {},
	"gpt-5.2-chat-latest":      {},
	"gpt-5.3-chat-latest":      {},
	"gpt-5.4":                  {},
	"gpt-5.4-mini":             {},
	"gpt-5.4-nano":             {},
	"gpt-5.5":                  {},
	"gpt-5.5-pro":              {},
	"gpt-5.6-cyber":            {},
	"gpt-5.6-luna":             {},
	"gpt-5.6-sol":              {},
	"gpt-5.6-terra":            {},
	"gpt-6-astra":              {},
	"gpt-6-cyber":              {},
	"gpt-6-luna":               {},
	"gpt-6-sol":                {},
	"gpt-6.1-sol":              {},
	"gpt-6.1-terra":            {},
	"gpt-6.2-astra":            {},
	"gpt-6.2-sol":              {},
	"gpt-daybreak-blue-latest": {},
	"gpt-daybreak-red-latest":  {},
	"gpt-oss-120b":             {},
	"gpt-oss-20b":              {},
	"o3":                       {},
	"o3-deep-research":         {},
	"o3-mini":                  {},
	"o4-mini":                  {},
	"o4-mini-deep-research":    {},
}

// supportsCodeExecution reports whether model is listed on OpenAI's official model pages as
// supporting the Responses code_interpreter tool.
func supportsCodeExecution(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if _, ok := codeInterpreterModels[m]; ok {
		return true
	}
	if base, ok := stripDateSuffix(m); ok {
		_, ok = codeInterpreterModels[base]
		return ok
	}
	return false
}

// stripDateSuffix removes a trailing -YYYY-MM-DD dated snapshot suffix from a model ID.
func stripDateSuffix(m string) (string, bool) {
	if len(m) <= 11 || m[len(m)-11] != '-' || m[len(m)-6] != '-' || m[len(m)-3] != '-' {
		return "", false
	}
	for _, i := range []int{10, 9, 8, 7, 5, 4, 2, 1} {
		c := m[len(m)-i]
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return m[:len(m)-11], true
}

// canonicalCodeExecutionTool returns the Responses code_interpreter tool JSON for a translated
// canonical code execution tool. The Responses API requires container; {"type":"auto"} selects
// the default auto-provisioned 1g container.
func canonicalCodeExecutionTool() json.RawMessage {
	return json.RawMessage(`{"type":"code_interpreter","container":{"type":"auto"}}`)
}
