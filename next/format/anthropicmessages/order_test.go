package anthropicmessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Regression pin for StreamWriter in render.go (not request encoding in encode.go or fit.go):
// replies from other formats reach a Messages client as blocks Anthropic's streaming protocol
// allows (https://platform.claude.com/docs/en/build-with-claude/streaming): one block open at a
// time with sequential indices, held deltas flushed when a queued block opens, and each thinking
// block's signature_delta written before its content_block_stop. The cases are the ones reported
// against today's core: a Gemini stream that ends on a signature-only thought after the text, a
// thought that starts while text is still open, and two Responses reasoning items in a row.
func TestStreamWriterThinkingBlockOrder(t *testing.T) {
	text := &llm.ItemStart{Kind: llm.ItemMessage}
	think := &llm.ItemStart{Kind: llm.ItemReasoning}
	for _, c := range []struct {
		name   string
		format string
		events []llm.Event
		blocks []string // "type=content" or "type=content:signature" in order
	}{
		{"gemini trailing signature", llm.FormatGeminiGenerate, []llm.Event{
			{Type: llm.EventItemStart, Index: 0, Item: text},
			{Type: llm.EventTextDelta, Index: 0, Delta: "Hi"},
			{Type: llm.EventItemEnd, Index: 0},
			{Type: llm.EventItemStart, Index: 1, Item: think},
			{Type: llm.EventItemEnd, Index: 1, Opaque: &llm.Opaque{Format: llm.FormatGeminiGenerate, Data: "gsig"}},
		}, []string{"text=Hi", "thinking=:gsig"}},
		{"thought while text is open", llm.FormatGeminiGenerate, []llm.Event{
			{Type: llm.EventItemStart, Index: 0, Item: text},
			{Type: llm.EventTextDelta, Index: 0, Delta: "Hi"},
			{Type: llm.EventItemStart, Index: 1, Item: think},
			{Type: llm.EventReasoningDelta, Index: 1, Delta: "hmm"},
			{Type: llm.EventTextDelta, Index: 0, Delta: " there"},
			{Type: llm.EventItemEnd, Index: 1, Opaque: &llm.Opaque{Format: llm.FormatGeminiGenerate, Data: "gsig"}},
			{Type: llm.EventItemEnd, Index: 0},
		}, []string{"text=Hi there", "thinking=hmm:gsig"}},
		{"two reasoning items", llm.FormatOpenAIResponses, []llm.Event{
			{Type: llm.EventItemStart, Index: 0, Item: think},
			{Type: llm.EventReasoningDelta, Index: 0, Delta: "a"},
			{Type: llm.EventItemEnd, Index: 0, Opaque: &llm.Opaque{Format: llm.OpaqueDevin, Data: "one"}},
			{Type: llm.EventItemStart, Index: 1, Item: think},
			{Type: llm.EventReasoningDelta, Index: 1, Delta: "b"},
			{Type: llm.EventItemEnd, Index: 1, Opaque: &llm.Opaque{Format: llm.OpaqueDevin, Data: "two"}},
			{Type: llm.EventItemStart, Index: 2, Item: text},
			{Type: llm.EventTextDelta, Index: 2, Delta: "ok"},
			{Type: llm.EventItemEnd, Index: 2},
		}, []string{"thinking=a:one", "thinking=b:two", "text=ok"}},
	} {
		events := append([]llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "up", Format: c.format}}}, c.events...)
		events = append(events, llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}})
		open, next := -1, 0
		var blocks []string
		body := map[int]string{}
		sig := map[int]string{}
		kind := map[int]string{}
		for _, line := range writeAll(t, events) {
			typ, data, _ := strings.Cut(line, " ")
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
				Delta struct {
					Type      string `json:"type"`
					Text      string `json:"text"`
					Thinking  string `json:"thinking"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			switch typ {
			case "content_block_start":
				if open >= 0 || ev.Index != next {
					t.Errorf("%s: block %d starts while block %d is open (next index %d)", c.name, ev.Index, open, next)
				}
				open, next = ev.Index, ev.Index+1
				kind[ev.Index] = ev.ContentBlock.Type
			case "content_block_delta":
				if ev.Index != open {
					t.Errorf("%s: a delta for block %d while block %d is open", c.name, ev.Index, open)
				}
				switch ev.Delta.Type {
				case "text_delta":
					body[ev.Index] += ev.Delta.Text
				case "thinking_delta":
					body[ev.Index] += ev.Delta.Thinking
				case "signature_delta":
					sig[ev.Index] = ev.Delta.Signature
				}
			case "content_block_stop":
				if ev.Index != open {
					t.Errorf("%s: block %d stops while block %d is open", c.name, ev.Index, open)
				}
				b := kind[ev.Index] + "=" + body[ev.Index]
				if s := sig[ev.Index]; s != "" {
					b += ":" + s
				}
				blocks = append(blocks, b)
				open = -1
			}
		}
		if open >= 0 {
			t.Errorf("%s: block %d never stops", c.name, open)
		}
		if strings.Join(blocks, ",") != strings.Join(c.blocks, ",") {
			t.Errorf("%s: blocks %q, want %q", c.name, blocks, c.blocks)
		}
	}
}
