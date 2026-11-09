package anthropicmessages

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// countFields are the fields of a Messages request that /v1/messages/count_tokens takes
// (Anthropic's API reference, and mcp_servers with its beta); it refuses the others (max_tokens,
// stream, sampling settings).
var countFields = []string{"model", "messages", "system", "tools", "tool_choice", "thinking", "output_config", "cache_control", "speed", "mcp_servers"}

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

// generationFields are the fields of a Messages request that only generating uses; count_tokens
// refuses them.
var generationFields = []string{"max_tokens", "stream", "temperature", "top_p", "top_k", "stop_sequences", "metadata", "service_tier"}

// CountBody returns a client's own Messages body for count_tokens: as it came, without the
// fields only generating uses, so a create request counted as it is gets a count; every other
// field goes on, so fields count_tokens takes that CountRequest does not list still reach it.
func CountBody(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	n := len(m)
	for _, k := range generationFields {
		delete(m, k)
	}
	if len(m) == n {
		return body, nil
	}
	return json.Marshal(m)
}
