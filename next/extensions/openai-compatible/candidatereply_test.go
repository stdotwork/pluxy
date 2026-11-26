package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Every candidate's tool call, not only the first, comes back under the client's tool name, for
// each upstream format and whether the reply streamed or not.
func TestReportingRestoresEveryCandidatesToolName(t *testing.T) {
	req := llm.Request{Tools: []llm.Tool{{Name: "lookup/item"}}}
	chatCall := func(i int) string {
		return `{"index":` + string(rune('0'+i)) + `,"message":{"role":"assistant","tool_calls":[{"id":"c` + string(rune('0'+i)) + `","type":"function","function":{"name":"lookup_item","arguments":"{}"}}]},"finish_reason":"tool_calls"}`
	}
	chatStreamCall := func(i int) string {
		return `{"index":` + string(rune('0'+i)) + `,"delta":{"tool_calls":[{"index":0,"id":"c` + string(rune('0'+i)) + `","type":"function","function":{"name":"lookup_item","arguments":"{}"}}]},"finish_reason":"tool_calls"}`
	}
	gemCall := func(i int) string {
		return `{"index":` + string(rune('0'+i)) + `,"content":{"parts":[{"functionCall":{"name":"lookup_item","args":{}}}]},"finishReason":"STOP"}`
	}
	cases := []struct {
		name, format, body string
		stream             bool
	}{
		{"chat whole", llm.FormatOpenAIChat, `{"id":"x","model":"m","choices":[` + chatCall(0) + `,` + chatCall(1) + `]}`, false},
		{"chat stream", llm.FormatOpenAIChat, `{"id":"x","model":"m","choices":[` + chatStreamCall(0) + `,` + chatStreamCall(1) + `]}`, true},
		{"gemini whole", llm.FormatGeminiGenerate, `{"candidates":[` + gemCall(0) + `,` + gemCall(1) + `]}`, false},
		{"gemini stream", llm.FormatGeminiGenerate, `{"candidates":[` + gemCall(0) + `,` + gemCall(1) + `]}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var events []llm.Event
			var err error
			if c.stream {
				dec := decoders[c.format].stream()
				events, err = dec.Decode([]byte(c.body))
				if err == nil {
					var more []llm.Event
					more, err = dec.Close()
					events = append(events, more...)
				}
			} else {
				events, err = decoders[c.format].response([]byte(c.body))
			}
			if err != nil {
				t.Fatal(err)
			}
			tr := &llm.Translation{To: c.format, From: llm.FormatAnthropicMessages}
			r := &reporting{names: replyNames(req, tr)}
			var names []string
			for _, ev := range events {
				r.restore(&ev)
				switch {
				case ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall:
					names = append(names, ev.Item.Name)
				case ev.Type == llm.EventCandidate && ev.Candidate.Kind == llm.CandidateToolCallStart:
					names = append(names, ev.Candidate.Name)
				}
			}
			if len(names) != 2 || names[0] != "lookup/item" || names[1] != "lookup/item" {
				t.Errorf("call names = %q, want both lookup/item", names)
			}
		})
	}
}
