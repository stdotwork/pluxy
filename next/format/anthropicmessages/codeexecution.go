package anthropicmessages

import (
	"encoding/json"
	"strings"
)

// codeExecutionToolType is the Claude server code execution tool a translated code execution tool
// becomes. Per https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool,
// code_execution_20250825 requires no anthropic-beta header and behaves the same across every
// supported model.
const (
	codeExecutionToolType = "code_execution_20250825"
	codeExecutionToolName = "code_execution"
)

// supportsCodeExecution reports whether model is in a Claude model family listed on Anthropic's
// code execution tool page (https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool):
// Claude 4.5 and later (Opus, Sonnet, Haiku, Fable, Mythos) and claude-mythos-preview.
func supportsCodeExecution(model string) bool {
	m := claudeName(model)
	if m == "claude-mythos-preview" || strings.HasPrefix(m, "claude-mythos-preview-") {
		return true
	}
	major, minor, ok := parseClaudeVersion(model)
	return ok && (major > 4 || (major == 4 && minor >= 5))
}

// canonicalCodeExecutionTool returns the Messages tool JSON for a translated canonical code
// execution tool.
func canonicalCodeExecutionTool() json.RawMessage {
	return json.RawMessage(`{"name":"` + codeExecutionToolName + `","type":"` + codeExecutionToolType + `"}`)
}
