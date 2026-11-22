package format_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Each block fits a frame, so the runs a stream collects must not outgrow one on finish.
func TestCodeExecutionsFitOneFrame(t *testing.T) {
	d := anthropicmessages.NewStreamDecoder()
	feed := func(s string) []llm.Event {
		t.Helper()
		evs, err := d.Decode([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	feed(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	big := strings.Repeat("x", 20<<20)
	for i := range 4 {
		id := fmt.Sprintf("srvtoolu_%d", i)
		feed(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"bash_code_execution","input":{"command":"ls"}}}`, i*2, id))
		feed(fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i*2))
		feed(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":%q,"content":{"type":"bash_code_execution_result","stdout":%q,"stderr":"","return_code":0,"content":[]}}}`, i*2+1, id, big))
		feed(fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i*2+1))
	}
	feed(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
	evs := feed(`{"type":"message_stop"}`)
	last := evs[len(evs)-1]
	b, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > protocol.MaxFrame/8 {
		t.Fatalf("finish event is %d MiB", len(b)>>20)
	}
	if n := len(last.CodeExecutions); n == 0 || !last.CodeExecutions[n-1].FilesOmitted {
		t.Fatalf("runs = %d, want a last record saying runs were left out", n)
	}
}

func geminiReplyFrom(t *testing.T, body string) (string, []llm.Loss) {
	t.Helper()
	evs, err := anthropicmessages.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	a := geminigenerate.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), a.Losses()
}

// Gemini's language enum has only Python, so a shell command must not be shown as Python.
func TestBashRunIsNotShownAsPython(t *testing.T) {
	out, losses := geminiReplyFrom(t, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[
		{"type":"server_tool_use","id":"srvtoolu_b","name":"bash_code_execution","input":{"command":"ls -la"}},
		{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_b","content":{"type":"bash_code_execution_result","stdout":"a","stderr":"","return_code":0,"content":[]}},
		{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	if strings.Contains(out, "executableCode") || strings.Contains(out, "codeExecutionResult") {
		t.Errorf("bash run shown as a Gemini code part: %s", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("text lost: %s", out)
	}
	if len(losses) == 0 {
		t.Error("no loss reported for the bash run")
	}
}

// A block the decoder cannot read still counts as lost for a Gemini client.
func TestUnreadCodeBlockIsReportedLost(t *testing.T) {
	_, losses := geminiReplyFrom(t, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[
		{"type":"server_tool_use","id":"srvtoolu_u","name":"bash_code_execution","input":{"command":"ls"}},
		{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_u","content":{"type":"future_result"}},
		{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	if len(losses) == 0 {
		t.Error("no loss reported for a code block that was not carried")
	}
}
