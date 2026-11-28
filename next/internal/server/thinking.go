package server

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// Gemini's model is in the path, not the body.
func takeThinkingSuffix(req *llm.Request) {
	model, suffix := llm.SplitThinkingSuffix(req.Model)
	if suffix == "" {
		return
	}
	req.Model = model
	req.Options.ThinkingSuffix = suffix
	req.Options.ReasoningEffort = llm.ThinkingEffort(suffix)
	n := req.Native
	if n == nil || n.Format == llm.FormatGeminiGenerate {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(n.Body, &m) != nil || m["model"] == nil {
		return
	}
	m["model"], _ = json.Marshal(model)
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	native := *n
	native.Body = body
	req.Native = &native
}
