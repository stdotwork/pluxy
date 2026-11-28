package anthropicmessages

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// countFields are the fields of a Messages request that /v1/messages/count_tokens takes
// (Anthropic's API reference, and mcp_servers with its beta); it refuses the others (max_tokens,
// stream, sampling settings).
var countFields = []string{"model", "messages", "system", "tools", "tool_choice", "thinking", "output_config", "cache_control", "speed", "mcp_servers"}

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

func DecodeCount(raw []byte) (int64, error) {
	var r struct {
		InputTokens *int64 `json:"input_tokens"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.InputTokens == nil {
		return 0, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "the upstream's count_tokens reply has no input_tokens", Raw: llm.RawJSON(raw)}
	}
	return *r.InputTokens, nil
}

var generationFields = []string{"max_tokens", "stream", "temperature", "top_p", "top_k", "stop_sequences", "metadata", "service_tier"}

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
