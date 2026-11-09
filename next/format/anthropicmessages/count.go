package anthropicmessages

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// countFields are the fields of a Messages request that /v1/messages/count_tokens takes; it
// refuses the others (max_tokens, stream, sampling settings).
var countFields = []string{"model", "messages", "system", "tools", "tool_choice", "thinking", "mcp_servers"}

// CountRequest returns the count_tokens body for a Messages request body: its fields that
// count_tokens takes.
func CountRequest(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(countFields))
	for _, k := range countFields {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return json.Marshal(out)
}

// DecodeCount reads a count_tokens reply: its input_tokens.
func DecodeCount(raw []byte) (int64, error) {
	var r struct {
		InputTokens *int64 `json:"input_tokens"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.InputTokens == nil {
		return 0, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "the upstream's count_tokens reply has no input_tokens", Raw: llm.RawJSON(raw)}
	}
	return *r.InputTokens, nil
}
