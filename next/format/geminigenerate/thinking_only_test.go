package geminigenerate

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
	a := NewAggregator()
	for _, ev := range onlyThinkingEvents() {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(out.Candidates) != 1 || len(out.Candidates[0].Content.Parts) != 1 {
		t.Fatalf("body %s: want one candidate holding one part", body)
	}
	p := out.Candidates[0].Content.Parts[0]
	if p.Text != "thinking" || !p.Thought || out.Candidates[0].FinishReason != "STOP" {
		t.Errorf("body %s: want the thought part and finishReason STOP", body)
	}

	s := writeStream(t, onlyThinkingEvents())
	if !strings.Contains(s, `"thought":true`) || !strings.Contains(s, `"finishReason":"STOP"`) {
		t.Errorf("stream %s: want the thought part and STOP", s)
	}
}
