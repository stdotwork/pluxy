package clienttools_test

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestLowerPreservesCodeExecutionTool(t *testing.T) {
	req := llm.Request{
		Model: "claude-sonnet-4-6",
		Tools: []llm.Tool{
			{
				Type: llm.ToolNative,
				Origin: &llm.Origin{
					Format: llm.FormatOpenAIResponses,
					Path:   "tools[0]",
					Raw:    json.RawMessage(`{"type":"custom","name":"apply_patch","description":"patch"}`),
				},
			},
			{
				Type: llm.ToolCodeExecution,
				Origin: &llm.Origin{
					Format: llm.FormatOpenAIResponses,
					Path:   "tools[1]",
					Raw:    json.RawMessage(`{"type":"code_interpreter","container":{"type":"auto"}}`),
				},
			},
		},
	}
	lowered, losses := clienttools.Lower(req)
	if len(losses) != 0 {
		t.Fatalf("unexpected losses: %+v", losses)
	}
	var hasCustom, hasCodeExec bool
	for _, tool := range lowered.Tools {
		switch {
		case tool.IsFunction() && tool.Name == "apply_patch":
			hasCustom = true
		case tool.Type == llm.ToolCodeExecution:
			hasCodeExec = true
		}
	}
	if !hasCustom || !hasCodeExec {
		t.Fatalf("tools after Lower = %+v, want apply_patch function and ToolCodeExecution", lowered.Tools)
	}
}
