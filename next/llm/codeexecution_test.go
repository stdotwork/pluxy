package llm_test

import (
	"encoding/json"
	"slices"
	"strings"
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

func TestCodeRunsLeaveOutALongRunAndKeepTheRest(t *testing.T) {
	long := llm.CodeExecution{Code: strings.Repeat("x", llm.MaxCodeExecutionCodeBytes+1)}
	var runs llm.CodeRuns
	runs.Append(llm.CodeExecution{Code: "a"})
	runs.Append(long)
	runs.Append(llm.CodeExecution{Code: "b"})
	want := []llm.CodeExecution{{Code: "a"}, {Code: "b"}, {FilesOmitted: true}}
	if got := runs.List(); !slices.Equal(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
	var atLimit llm.CodeRuns
	atLimit.Append(llm.CodeExecution{Code: strings.Repeat("x", llm.MaxCodeExecutionCodeBytes)})
	if got := atLimit.List(); len(got) != 1 || got[0].FilesOmitted {
		t.Errorf("code at the limit is left out: %d runs", len(got))
	}
}

func TestCodeRunsLeaveOutEndsTheListOnce(t *testing.T) {
	var runs llm.CodeRuns
	runs.Append(llm.CodeExecution{Code: "a"})
	runs.LeaveOut()
	runs.LeaveOut()
	if want := []llm.CodeExecution{{Code: "a"}, {FilesOmitted: true}}; !slices.Equal(runs.List(), want) {
		t.Errorf("runs = %+v, want %+v", runs.List(), want)
	}
	var none llm.CodeRuns
	if none.List() != nil {
		t.Errorf("no runs = %+v, want nil", none.List())
	}
	none.LeaveOut()
	if want := []llm.CodeExecution{{FilesOmitted: true}}; !slices.Equal(none.List(), want) {
		t.Errorf("only the record = %+v, want %+v", none.List(), want)
	}
}

func TestCodeRunsRemoveTakesBackARun(t *testing.T) {
	var runs llm.CodeRuns
	for _, c := range []string{"a", "b", "a"} {
		runs.Append(llm.CodeExecution{Code: c})
	}
	runs.Remove(llm.CodeExecution{Code: "a"})
	runs.Remove(llm.CodeExecution{Code: "zz"})
	if want := []llm.CodeExecution{{Code: "a"}, {Code: "b"}}; !slices.Equal(runs.List(), want) {
		t.Errorf("runs = %+v, want %+v", runs.List(), want)
	}
}
