package protocol

import (
	"bytes"
	"encoding/json"
	"strings"

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

// FitFinish makes the finish event ev fit one frame. It leaves the runs out first, keeping the one
// record that says they were left out, and then the sources. It returns a loss naming what it left
// out, and false if it left out nothing.
func FitFinish(ev *llm.Event) (llm.Loss, bool) {
	limit := MaxFrame - FrameHeadroom
	size := len(ev.Raw) + JSONSize(ev.Sources) + JSONSize(ev.CodeExecutions)
	if size <= limit {
		return llm.Loss{}, false
	}
	var left []string
	if len(ev.CodeExecutions) > 0 && !(len(ev.CodeExecutions) == 1 && ev.CodeExecutions[0] == (llm.CodeExecution{FilesOmitted: true})) {
		ev.CodeExecutions = llm.CodeExecutions{{FilesOmitted: true}}
		left = append(left, "code execution runs")
		size = len(ev.Raw) + JSONSize(ev.Sources) + JSONSize(ev.CodeExecutions)
	}
	if size > limit && len(ev.Sources) > 0 {
		ev.Sources = nil
		left = append(left, "sources")
	}
	if len(left) == 0 {
		return llm.Loss{}, false
	}
	return llm.Loss{Kind: llm.LossDropped, Path: llm.LossPathFinish,
		Detail: "the reply's " + strings.Join(left, " and ") + " are left out: with the reply they would not fit one protocol frame"}, true
}
