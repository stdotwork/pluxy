package llm_test

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestEventCodeExecutionsJSONRoundTrip(t *testing.T) {
	fin := llm.Event{
		Type:   llm.EventFinish,
		Finish: &llm.Finish{Reason: llm.FinishStop},
		CodeExecutions: llm.CodeExecutions{
			{
				Code:         "print(2 + 2)",
				Language:     "python",
				Output:       "4\n",
				Failed:       false,
				FilesOmitted: true,
			},
			{
				Code:     "1 / 0",
				Language: "python",
				Output:   "ZeroDivisionError: division by zero",
				Failed:   true,
			},
		},
	}
	b, err := json.Marshal(fin)
	if err != nil {
		t.Fatalf("marshal finish: %v", err)
	}
	var got llm.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal finish: %v", err)
	}
	if len(got.CodeExecutions) != 2 {
		t.Fatalf("round-trip code_executions len = %d, want 2", len(got.CodeExecutions))
	}
	if got.CodeExecutions[0] != fin.CodeExecutions[0] {
		t.Errorf("code_executions[0] = %+v, want %+v", got.CodeExecutions[0], fin.CodeExecutions[0])
	}
	if got.CodeExecutions[1] != fin.CodeExecutions[1] {
		t.Errorf("code_executions[1] = %+v, want %+v", got.CodeExecutions[1], fin.CodeExecutions[1])
	}
}

func TestCodeExecutionsEmptyListParsesToNil(t *testing.T) {
	var ev llm.Event
	if err := json.Unmarshal([]byte(`{"type":"finish","code_executions":[]}`), &ev); err != nil {
		t.Fatalf("unmarshal empty code_executions: %v", err)
	}
	if ev.CodeExecutions != nil {
		t.Errorf("empty code_executions = %#v, want nil", ev.CodeExecutions)
	}
	if err := json.Unmarshal([]byte(`{"type":"finish","code_executions":null}`), &ev); err != nil {
		t.Fatalf("unmarshal null code_executions: %v", err)
	}
	if ev.CodeExecutions != nil {
		t.Errorf("null code_executions = %#v, want nil", ev.CodeExecutions)
	}
}
