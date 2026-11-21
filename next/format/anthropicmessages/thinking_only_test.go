package anthropicmessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// onlyThinkingEvents is a reply that reasons and then stops, with no text and no tool call.
func onlyThinkingEvents() []llm.Event {
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "msg_1", Model: "m", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "thinking"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
}

// A reply that only thinks is a complete Message: its content is the thinking block and it
// carries its stop reason.
func TestReasoningOnlyReplyIsComplete(t *testing.T) {
	a := NewAggregator()
	for _, ev := range onlyThinkingEvents() {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "thinking" || out.Content[0].Thinking != "thinking" {
		t.Errorf("body %s: want the thinking block", body)
	}
	if out.StopReason != "end_turn" {
		t.Errorf("body %s: stop_reason = %q, want end_turn", body, out.StopReason)
	}

	lines := writeAll(t, onlyThinkingEvents())
	if len(lines) < 3 {
		t.Fatalf("stream has %d events: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[1], "content_block_start ") || !strings.Contains(lines[1], `"type":"thinking"`) {
		t.Errorf("second event %q is not the thinking block's start", lines[1])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "message_stop ") {
		t.Errorf("last event %q is not message_stop", lines[len(lines)-1])
	}
}
