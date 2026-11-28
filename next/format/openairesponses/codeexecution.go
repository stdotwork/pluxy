package openairesponses

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

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

func canonicalCodeExecutionTool() json.RawMessage {
	return json.RawMessage(`{"type":"code_interpreter","container":{"type":"auto"}}`)
}

// codeInterpreterOutputs is the include value that makes a code_interpreter_call item carry its
// outputs. The Responses create reference lists it under include, and a call returns outputs only
// when it is asked for: https://developers.openai.com/api/reference/resources/responses/create
const codeInterpreterOutputs = "code_interpreter_call.outputs"

func withInclude(client *llm.Origin, value string) []json.RawMessage {
	var out []json.RawMessage
	if client != nil && client.Format == format {
		_ = json.Unmarshal(rawjson.Object(client.Raw)["include"], &out)
	}
	want, _ := json.Marshal(value)
	for _, v := range out {
		var s string
		if json.Unmarshal(v, &s) == nil && s == value {
			return out
		}
	}
	return append(out, want)
}
