package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func onlyThinkingEvents() []llm.Event {
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatOpenAIChat}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
		{Type: llm.EventReasoningDelta, Index: 0, Delta: "thinking"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
}

func TestReasoningOnlyReplyIsComplete(t *testing.T) {
	a := NewAggregator(llm.Request{Model: "m"})
	for _, ev := range onlyThinkingEvents() {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if out.Status != "completed" || len(out.Output) != 1 || out.Output[0].Type != "reasoning" ||
		len(out.Output[0].Summary) != 1 || out.Output[0].Summary[0].Text != "thinking" {
		t.Errorf("body %s: want a completed response whose output is the reasoning", body)
	}

	var stream strings.Builder
	w := NewStreamWriter(&stream, llm.Request{Model: "m"})
	for _, ev := range onlyThinkingEvents() {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	s := stream.String()
	if !strings.Contains(s, "event: response.completed") || !strings.Contains(s, `"type":"reasoning"`) || !strings.Contains(s, `"status":"completed"`) {
		t.Errorf("stream %s: want a completed response with the reasoning item", s)
	}
}
