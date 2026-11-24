package anthropicmessages

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func codeRunsStreamed(t *testing.T, input string) []llm.CodeExecution {
	t.Helper()
	d := NewStreamDecoder()
	var last []llm.Event
	feed := func(s string) {
		t.Helper()
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) > 0 {
			last = evs
		}
	}
	feed(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	feed(`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{}}}`)
	const piece = 1 << 20
	for i := 0; i < len(input); i += piece {
		feed(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, input[i:min(i+piece, len(input))]))
	}
	feed(`{"type":"content_block_stop","index":0}`)
	feed(`{"type":"content_block_start","index":1,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"1\n","stderr":"","return_code":0,"content":[]}}}`)
	feed(`{"type":"content_block_stop","index":1}`)
	feed(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
	feed(`{"type":"message_stop"}`)
	return last[len(last)-1].CodeExecutions
}

func codeRunsWhole(t *testing.T, input string) []llm.CodeExecution {
	t.Helper()
	evs, err := DecodeResponse([]byte(`{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":` + input + `},
		{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"1\n","stderr":"","return_code":0,"content":[]}}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	return evs[len(evs)-1].CodeExecutions
}

// A field that is not the code must not decide whether the run is kept, so a stream and the same
// reply not streamed keep the same runs.
func TestCodeInputWithAHugeOtherFieldIsKeptTheSameStreamedAndNot(t *testing.T) {
	for _, padSize := range []int{1 << 10, 1 << 20, maxCodeExecInput - 100, maxCodeExecInput, 7 << 20} {
		input := `{"code":"print(1)","pad":"` + strings.Repeat("p", padSize) + `"}`
		whole, streamed := codeRunsWhole(t, input), codeRunsStreamed(t, input)
		if !slices.Equal(whole, streamed) {
			t.Errorf("pad %d: not streamed %+v, streamed %+v", padSize, whole, streamed)
		}
		if kept := len(whole) > 0 && whole[0].Code == "print(1)"; kept != (len(input) <= maxCodeExecInput) {
			t.Errorf("pad %d: kept = %v for an input of %d bytes", padSize, kept, len(input))
		}
	}
}

// Code blocks that never close hold their input together within one block's bound.
func TestOpenCodeBlocksHoldOneBoundBetweenThem(t *testing.T) {
	d := NewStreamDecoder()
	feed := func(s string) {
		t.Helper()
		if _, err := d.Decode([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	feed(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	piece := strings.Repeat("x", 1<<20)
	for i := range 40 {
		feed(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"srvtoolu_%d","name":"code_execution","input":{}}}`, i, i))
		for range 5 {
			feed(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, i, piece))
		}
	}
	held := 0
	for _, pc := range d.codeBlock {
		held += pc.partial.Len() + len(pc.initialInput)
	}
	if held > maxCodeExecInput || d.codeHeld > maxCodeExecInput {
		t.Errorf("open blocks hold %d bytes (counted %d), over %d", held, d.codeHeld, maxCodeExecInput)
	}
}
