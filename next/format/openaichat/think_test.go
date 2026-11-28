package openaichat

import (
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	openTag  = "<" + "think" + ">"
	closeTag = "<" + "/think" + ">"
)

func TestThinkTagsBecomeReasoning(t *testing.T) {
	t.Run("nonstream", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"`+openTag+`x`+closeTag+`y"}`))
		if got != "R:x|T:y" {
			t.Errorf("got %q, want R:x|T:y", got)
		}
	})
	t.Run("nonstream after leading whitespace", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":" \n`+openTag+`x`+closeTag+`y"}`))
		if got != "R:x|T:y" {
			t.Errorf("got %q, want R:x|T:y", got)
		}
	})
	t.Run("nonstream unterminated", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"`+openTag+`rest"}`))
		if got != "R:rest" {
			t.Errorf("got %q, want R:rest", got)
		}
	})
	t.Run("nonstream tag later is text", func(t *testing.T) {
		got := responseEvents(t, completionBody(`{"role":"assistant","content":"Hi `+openTag+`x`+closeTag+`"}`))
		if want := "T:Hi " + openTag + "x" + closeTag; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("stream split tag", func(t *testing.T) {
		got := reasoningEvents(t,
			deltaChunk(`{"content":"`+openTag[:4]+`"}`),
			deltaChunk(`{"content":"`+openTag[4:]+`reason"}`),
			deltaChunk(`{"content":"ing`+closeTag[:5]+`"}`),
			deltaChunk(`{"content":"`+closeTag[5:]+`answer"}`),
			finishChunk)
		if got != "R:reason|R:ing|T:answer" {
			t.Errorf("got %q, want R:reason|R:ing|T:answer", got)
		}
	})
	t.Run("stream ordinary text is not held", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"content":"H"}`), deltaChunk(`{"content":"i"}`), finishChunk)
		if got != "T:H|T:i" {
			t.Errorf("got %q, want T:H|T:i", got)
		}
	})
	t.Run("stream diverging tag prefix is text", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"content":"`+openTag[:4]+`"}`), deltaChunk(`{"content":"x"}`), finishChunk)
		if got != "T:"+openTag[:4]+"x" {
			t.Errorf("got %q, want T:%sx", got, openTag[:4])
		}
	})
	t.Run("stream flush mid tag", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"content":"`+openTag+`unfinished"}`), finishChunk)
		if got != "R:unfinished" {
			t.Errorf("got %q, want R:unfinished", got)
		}
	})
	t.Run("stream flush partial open tag", func(t *testing.T) {
		got := reasoningEvents(t, deltaChunk(`{"content":"`+openTag[:4]+`"}`), finishChunk)
		if got != "T:"+openTag[:4] {
			t.Errorf("got %q, want T:%s", got, openTag[:4])
		}
	})
}

func significantEvents(t *testing.T, chunks ...string) []string {
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
			case llm.EventTextDelta:
				got = append(got, "T:"+ev.Delta)
			case llm.EventReasoningDelta:
				got = append(got, "R:"+ev.Delta)
			case llm.EventItemStart:
				got = append(got, "I:"+ev.Item.Kind)
			}
		}
	}
	return got
}

func TestHeldThinkContentGoesBeforeInterruptions(t *testing.T) {
	call := `{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}`
	callFinish := `{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	t.Run("whitespace before a tool call", func(t *testing.T) {
		got := significantEvents(t, deltaChunk(`{"content":"  "}`), call, callFinish)
		text, start := slices.Index(got, "T:  "), slices.Index(got, "I:tool_call")
		if text < 0 || start < 0 || text > start {
			t.Errorf("events %q: want the held whitespace before the tool call", got)
		}
	})
	t.Run("partial tag before a tool call", func(t *testing.T) {
		got := significantEvents(t, deltaChunk(`{"content":"`+openTag[:4]+`"}`), call, callFinish)
		text, start := slices.Index(got, "T:"+openTag[:4]), slices.Index(got, "I:tool_call")
		if text < 0 || start < 0 || text > start {
			t.Errorf("events %q: want the held text before the tool call", got)
		}
	})
	t.Run("held reasoning before a refusal", func(t *testing.T) {
		got := significantEvents(t,
			deltaChunk(`{"content":"`+openTag+`thinking`+closeTag[:5]+`"}`),
			deltaChunk(`{"refusal":"No."}`),
			finishChunk)
		text := slices.Index(got, "T:No.")
		if text < 0 {
			t.Fatalf("events %q: no refusal text", got)
		}
		for i, s := range got {
			if strings.HasPrefix(s, "R:") && i > text {
				t.Errorf("events %q: reasoning %q came after the refusal", got, s)
			}
		}
	})
}
