package anthropicmessages

import (
	"encoding/json"
	"strings"
)

const (
	codeExecutionToolType = "code_execution_20250825"
	codeExecutionToolName = "code_execution"
)

func supportsCodeExecution(model string) bool {
	m := claudeName(model)
	if m == "claude-mythos-preview" || strings.HasPrefix(m, "claude-mythos-preview-") {
		return true
	}
	major, minor, ok := parseClaudeVersion(model)
	return ok && (major > 4 || (major == 4 && minor >= 5))
}

func canonicalCodeExecutionTool() json.RawMessage {
	return json.RawMessage(`{"name":"` + codeExecutionToolName + `","type":"` + codeExecutionToolType + `"}`)
}
