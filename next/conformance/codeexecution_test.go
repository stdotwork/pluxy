package conformance

import (
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformanceCodeExecutions(t *testing.T) {
	if protocol.Minor < host.CodeExecutionsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.CodeExecutionsMinor)
	}
	valid := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "4"},
		{Type: llm.EventItemEnd, Index: 0},
		{
			Type:   llm.EventFinish,
			Finish: &llm.Finish{Reason: llm.FinishStop},
			CodeExecutions: llm.CodeExecutions{
				{Code: "print(2+2)", Language: "python", Output: "4\n"},
			},
		},
	}
	if err := checkReplyCodeExecutions(valid); err != nil {
		t.Fatalf("checkReplyCodeExecutions(valid) = %v, want nil", err)
	}

	invalidCases := []struct {
		name string
		evs  []llm.Event
	}{
		{
			name: "code_executions on item.end",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
				{Type: llm.EventItemEnd, Index: 0, CodeExecutions: llm.CodeExecutions{{Code: "print(1)", Output: "1"}}},
			},
		},
		{
			name: "code_executions on response.start",
			evs: []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1"}, CodeExecutions: llm.CodeExecutions{{Code: "x", Output: "y"}}},
			},
		},
		{
			name: "empty code execution record",
			evs: []llm.Event{
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, CodeExecutions: llm.CodeExecutions{{}}},
			},
		},
	}
	for _, tc := range invalidCases {
		if err := checkReplyCodeExecutions(tc.evs); err == nil {
			t.Errorf("%s: checkReplyCodeExecutions returned nil, want error", tc.name)
		}
	}
}
