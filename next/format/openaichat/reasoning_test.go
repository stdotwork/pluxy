package openaichat

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// reasoningEvents returns the reasoning and text deltas a set of stream chunks produced, in
// order, as "R:<reasoning>" and "T:<text>".
func reasoningEvents(t *testing.T, chunks ...string) string {
	t.Helper()
	d := NewStreamDecoder()
	var got []string
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode %s: %v", c, err)
		}
		for _, ev := range evs {
			switch ev.Type {
			case llm.EventReasoningDelta:
				got = append(got, "R:"+ev.Delta)
			case llm.EventTextDelta:
				got = append(got, "T:"+ev.Delta)
			}
		}
	}
	return strings.Join(got, "|")
}

// responseEvents returns the reasoning and text deltas a non-streamed reply produced, in order,
// as "R:<reasoning>" and "T:<text>".
func responseEvents(t *testing.T, body string) string {
	t.Helper()
	evs, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	var got []string
	for _, ev := range evs {
		switch ev.Type {
		case llm.EventReasoningDelta:
			got = append(got, "R:"+ev.Delta)
		case llm.EventTextDelta:
			got = append(got, "T:"+ev.Delta)
		}
	}
	return strings.Join(got, "|")
}

// deltaChunk wraps a Chat stream delta object as one chunk of a reply.
func deltaChunk(delta string) string {
	return `{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

// finishChunk is the chunk that ends a reply.
const finishChunk = `{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func completionBody(message string) string {
	return `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":` + message + `,"finish_reason":"stop"}]}`
}

// Reasoning arrives in more than one field: reasoning_content (DeepSeek and others), reasoning
// (vLLM, GLM) and reasoning_details (OpenRouter), and reasoning_content may be a list of parts.
func TestReasoningFieldVariants(t *testing.T) {
	t.Run("nonstream reasoning", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"Hi","reasoning":"plan"}`))
		if got != "R:plan|T:Hi" {
			t.Errorf("got %q, want R:plan|T:Hi", got)
		}
	})
	t.Run("nonstream reasoning_details", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"Hi","reasoning_details":[`+
			`{"type":"reasoning.text","text":"a","signature":"sig"},`+
			`{"type":"reasoning.summary","summary":"b"},`+
			`{"type":"reasoning.encrypted","data":"U0VDUkVU"}]}`))
		if got != "R:ab|T:Hi" {
			t.Errorf("got %q, want R:ab|T:Hi", got)
		}
		if strings.Contains(got, "U0VDUkVU") {
			t.Errorf("encrypted reasoning was shown as text: %q", got)
		}
	})
	t.Run("nonstream reasoning_content parts", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"Hi","reasoning_content":[`+
			`{"type":"reasoning.text","text":"p"},{"type":"text","text":"q"}]}`))
		if got != "R:pq|T:Hi" {
			t.Errorf("got %q, want R:pq|T:Hi", got)
		}
	})
	t.Run("nonstream same text in two fields", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"Hi","reasoning_content":"same","reasoning":"same",`+
			`"reasoning_details":[{"type":"reasoning.text","text":"same"}]}`))
		if got != "R:same|T:Hi" {
			t.Errorf("got %q, want the same text once: R:same|T:Hi", got)
		}
	})
	t.Run("stream reasoning", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"reasoning":"think"}`), finishChunk)
		if got != "R:think" {
			t.Errorf("got %q, want R:think", got)
		}
	})
	t.Run("stream reasoning_details", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"reasoning_details":[`+
			`{"type":"reasoning.text","text":"a"},`+
			`{"type":"reasoning.summary","summary":"b"},`+
			`{"type":"reasoning.encrypted","data":"U0VDUkVU"}]}`), finishChunk)
		if got != "R:ab" {
			t.Errorf("got %q, want R:ab", got)
		}
		if strings.Contains(got, "U0VDUkVU") {
			t.Errorf("encrypted reasoning was shown as text: %q", got)
		}
	})
	t.Run("stream reasoning_content parts", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"reasoning_content":[`+
			`{"type":"reasoning.text","text":"p"},{"type":"text","text":"q"}]}`), finishChunk)
		if got != "R:pq" {
			t.Errorf("got %q, want R:pq", got)
		}
	})
	t.Run("stream same text in two fields", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"reasoning":"same","reasoning_content":"same"}`), finishChunk)
		if got != "R:same" {
			t.Errorf("got %q, want the same text once: R:same", got)
		}
	})
}
