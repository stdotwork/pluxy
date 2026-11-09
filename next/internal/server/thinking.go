package server

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// takeThinkingSuffix takes a thinking suffix, as in gpt-5(high), off the request's model name and
// passes it in Options.ThinkingSuffix, as today's core reads it: the request is routed by the
// model's own name, and each extension applies the suffix to its upstream request. The effort the
// suffix comes to also replaces Options.ReasoningEffort, so an extension that reads only the
// effort (or was written before the suffix) still applies it. The model in
// a native body is renamed too, so a body sent unchanged names the model without the suffix.
// (Gemini's model is in the path, not the body.)
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
