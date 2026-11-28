package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func onlyThinkingEvents() []llm.Event {
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "c1", Model: "m", Format: llm.FormatOpenAIChat}},
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
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent string  `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content == nil || *out.Choices[0].Message.Content != "" {
		t.Errorf("body %s: want one choice whose message has a content field", body)
	}
	if out.Choices[0].Message.ReasoningContent != "thinking" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("body %s: want the reasoning and finish_reason stop", body)
	}

	var stream strings.Builder
	w := NewStreamWriter(&stream, false)
	for _, ev := range onlyThinkingEvents() {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	s := stream.String()
	if !strings.Contains(s, `"reasoning_content":"thinking"`) || !strings.Contains(s, `"finish_reason":"stop"`) || !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Errorf("stream %s: want the reasoning, the finish and [DONE]", s)
	}
}
