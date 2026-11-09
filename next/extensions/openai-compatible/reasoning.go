package main

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// applyReasoning applies a thinking suffix on the model name (llm.Options.ThinkingSuffix) to the
// upstream body, after the body is built (sent unchanged or translated), as today's core does for
// an API-key upstream: it wins over the body's own settings, as reasoning_effort for Chat,
// reasoning.effort for Responses, and for Messages and GenerateContent as the model's thinking
// settings (Claude's and Gemini's rules).
func applyReasoning(body []byte, req llm.Request, target string) ([]byte, error) {
	suffix := req.Options.ThinkingSuffix
	if suffix == "" {
		return body, nil
	}
	switch target {
	case llm.FormatAnthropicMessages:
		out, _, err := anthropicmessages.ApplyEffort(body, req.Model, suffix)
		return out, err
	case llm.FormatGeminiGenerate:
		out, _, err := geminigenerate.ApplyEffort(body, req.Model, suffix)
		return out, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, nil
	}
	v, _ := json.Marshal(llm.ThinkingEffort(suffix))
	if target == llm.FormatOpenAIChat {
		m["reasoning_effort"] = v
	} else {
		r := map[string]json.RawMessage{}
		_ = json.Unmarshal(m["reasoning"], &r)
		r["effort"] = v
		m["reasoning"], _ = json.Marshal(r)
	}
	return json.Marshal(m)
}
