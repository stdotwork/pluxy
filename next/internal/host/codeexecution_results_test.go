package host

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestCodeExecutionsMinorGating(t *testing.T) {
	if CodeExecutionsMinor != 32 {
		t.Fatalf("CodeExecutionsMinor = %d, want 32", CodeExecutionsMinor)
	}
	if protocol.Minor < CodeExecutionsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, CodeExecutionsMinor)
	}
	fin := llm.Event{
		Type:   llm.EventFinish,
		Finish: &llm.Finish{Reason: llm.FinishStop},
		CodeExecutions: llm.CodeExecutions{
			{Code: "print(4)", Language: "python", Output: "4\n"},
		},
	}

	oldExt := &Extension{info: protocol.Info{ProtocolMinor: CodeExecutionsMinor - 1}}
	gotOld := oldExt.normalizeEvent(fin)
	if gotOld.CodeExecutions != nil {
		t.Errorf("minor %d kept CodeExecutions: %+v, want nil", CodeExecutionsMinor-1, gotOld.CodeExecutions)
	}

	newExt := &Extension{info: protocol.Info{ProtocolMinor: CodeExecutionsMinor}}
	gotNew := newExt.normalizeEvent(fin)
	if len(gotNew.CodeExecutions) != 1 || gotNew.CodeExecutions[0].Code != "print(4)" {
		t.Errorf("minor %d CodeExecutions = %+v, want preserved", CodeExecutionsMinor, gotNew.CodeExecutions)
	}
}
