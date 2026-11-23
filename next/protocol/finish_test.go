package protocol

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestFitFinishKeepsWhatFits(t *testing.T) {
	ev := llm.Event{Type: llm.EventFinish, Raw: []byte(`{"a":1}`),
		Sources: llm.Sources{{URL: "https://s.example/1"}}, CodeExecutions: llm.CodeExecutions{{Code: "print(1)"}}}
	if _, cut := FitFinish(&ev); cut || len(ev.Sources) != 1 || len(ev.CodeExecutions) != 1 {
		t.Fatalf("a small finish was changed: cut=%v %+v", cut, ev)
	}
}

func TestFitFinishLeavesRunsOutBeforeSources(t *testing.T) {
	// A body that fills the frame but for 2 MiB: 3 MiB of runs do not fit and 1 MiB of sources do.
	ev := llm.Event{Type: llm.EventFinish, Raw: []byte(strings.Repeat("x", MaxFrame-FrameHeadroom-2<<20)),
		Sources:        llm.Sources{{URL: "https://s.example/1", Title: strings.Repeat("t", 1<<20)}},
		CodeExecutions: llm.CodeExecutions{{Code: strings.Repeat("c", 3<<20)}}}
	loss, cut := FitFinish(&ev)
	if !cut || loss.Kind != llm.LossDropped {
		t.Fatalf("cut=%v loss=%+v", cut, loss)
	}
	if len(ev.Sources) != 1 {
		t.Errorf("sources = %d, want them kept: they fit once the runs are left out", len(ev.Sources))
	}
	if len(ev.CodeExecutions) != 1 || ev.CodeExecutions[0] != (llm.CodeExecution{FilesOmitted: true}) {
		t.Errorf("runs = %+v, want the one record that says they were left out", ev.CodeExecutions)
	}
	if n := JSONSize(ev); n > MaxFrame {
		t.Errorf("finish is %d bytes, over MaxFrame", n)
	}
}
