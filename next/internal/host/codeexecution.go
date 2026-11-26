package host

import (
	"cmp"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// codeExecutionMinor is the protocol minor version that adds the canonical code execution tool
// (llm.ToolCodeExecution) and its forced tool choice (llm.ToolChoiceCodeExecution).
const codeExecutionMinor = 30

// downgradeCodeExecution returns req as an extension that implements protocol minor version minor
// can take it. Before codeExecutionMinor, every ToolCodeExecution becomes a ToolNative with its
// Origin unchanged, and a ToolChoiceCodeExecution returns to its pre-0.30 representation: for
// OpenAI Responses, ToolChoice is cleared and {"type":"code_interpreter"} is restored into
// req.Origin.Raw under "tool_choice"; for other formats (such as Anthropic Messages), Mode becomes
// ToolChoiceTool with Name preserved (defaulting to "code_execution"). req's own Tools slice,
// ToolChoice and Origin are never mutated.
func downgradeCodeExecution(minor int, req llm.Request) llm.Request {
	if minor >= codeExecutionMinor {
		return req
	}
	hasCodeExec := false
	for _, t := range req.Tools {
		if t.Type == llm.ToolCodeExecution {
			hasCodeExec = true
			break
		}
	}
	hasChoice := req.Options.ToolChoice != nil && req.Options.ToolChoice.Mode == llm.ToolChoiceCodeExecution
	if !hasCodeExec && !hasChoice {
		return req
	}
	if hasCodeExec {
		tools := make([]llm.Tool, len(req.Tools))
		for i, t := range req.Tools {
			if t.Type == llm.ToolCodeExecution {
				tools[i] = llm.Tool{Type: llm.ToolNative, Origin: t.Origin}
			} else {
				tools[i] = t
			}
		}
		req.Tools = tools
	}
	if hasChoice {
		if req.SourceFormat() == llm.FormatOpenAIResponses {
			req.Options.ToolChoice = nil
			m := map[string]json.RawMessage{}
			if req.Origin != nil && len(req.Origin.Raw) > 0 {
				_ = json.Unmarshal(req.Origin.Raw, &m)
			}
			m["tool_choice"] = json.RawMessage(`{"type":"code_interpreter"}`)
			if raw, err := json.Marshal(m); err == nil {
				var o llm.Origin
				if req.Origin != nil {
					o = *req.Origin
				} else {
					o.Format = llm.FormatOpenAIResponses
				}
				o.Raw = raw
				req.Origin = &o
			}
		} else {
			tc := *req.Options.ToolChoice
			tc.Mode = llm.ToolChoiceTool
			tc.Name = cmp.Or(tc.Name, "code_execution")
			req.Options.ToolChoice = &tc
		}
	}
	return req
}
