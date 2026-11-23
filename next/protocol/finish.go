package protocol

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// FrameHeadroom is what a decoder leaves free in a frame for the fields around the data it puts
// there: ids, usage and the rest of the event.
const FrameHeadroom = 1 << 20

// JSONSize returns the number of bytes v takes in a frame, which does not escape HTML.
func JSONSize(v any) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return 0
	}
	return buf.Len() - 1
}

// FitFinish makes the finish event ev fit one frame. A same-format reply carries the whole upstream
// body in Raw, which holds the reply's sources and code execution runs already, so what ev adds
// beside Raw could push its frame over MaxFrame for a reply the upstream sent within the limit. It
// leaves the runs out first, keeping the one record that says they were left out, and then the
// sources. It returns a loss for what it left out, and false if it left out nothing.
func FitFinish(ev *llm.Event) (llm.Loss, bool) {
	limit := MaxFrame - FrameHeadroom
	size := len(ev.Raw) + JSONSize(ev.Sources) + JSONSize(ev.CodeExecutions)
	if size <= limit {
		return llm.Loss{}, false
	}
	if len(ev.CodeExecutions) > 0 {
		ev.CodeExecutions = llm.CodeExecutions{{FilesOmitted: true}}
		size = len(ev.Raw) + JSONSize(ev.Sources) + JSONSize(ev.CodeExecutions)
	}
	if size > limit {
		ev.Sources = nil
	}
	return llm.Loss{Kind: llm.LossDropped, Path: "finish",
		Detail: "the reply's code execution runs and sources are left out: with the reply they would not fit one protocol frame"}, true
}
