package openaichat

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// textAndFinish returns the text deltas of a reply joined, and its finish.
func textAndFinish(t *testing.T, evs []llm.Event) (string, *llm.Finish) {
	t.Helper()
	var b strings.Builder
	var f *llm.Finish
	for _, ev := range evs {
		switch ev.Type {
		case llm.EventTextDelta:
			b.WriteString(ev.Delta)
		case llm.EventFinish:
			f = ev.Finish
		}
	}
	return b.String(), f
}

func streamReply(t *testing.T, chunks ...string) []llm.Event {
	t.Helper()
	d := NewStreamDecoder()
	var out []llm.Event
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode %s: %v", c, err)
		}
		out = append(out, evs...)
	}
	evs, err := d.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	return append(out, evs...)
}

// A refusal the upstream sends in the message, or in a delta, reaches the client as a refusal:
// its text is the reply's, and the reply finishes as a content filter, which a Messages client
// renders as stop_reason "refusal".
func TestRefusalReachesClient(t *testing.T) {
	t.Run("nonstream", func(t *testing.T) {
		evs, err := DecodeResponse([]byte(completionBody(`{"role":"assistant","content":null,"refusal":"I cannot help with that."}`)))
		if err != nil {
			t.Fatal(err)
		}
		text, finish := textAndFinish(t, evs)
		if text != "I cannot help with that." {
			t.Errorf("text = %q, want the refusal text", text)
		}
		if finish == nil || finish.Reason != llm.FinishContentFilter {
			t.Errorf("finish = %+v, want content_filter", finish)
		}
	})
	t.Run("stream", func(t *testing.T) {
		evs := streamReply(t, deltaChunk(`{"refusal":"I cannot help with that."}`), finishChunk)
		text, finish := textAndFinish(t, evs)
		if text != "I cannot help with that." {
			t.Errorf("text = %q, want the refusal text", text)
		}
		if finish == nil || finish.Reason != llm.FinishContentFilter {
			t.Errorf("finish = %+v, want content_filter", finish)
		}
	})
	t.Run("nonstream after content", func(t *testing.T) {
		evs, err := DecodeResponse([]byte(completionBody(`{"role":"assistant","content":"Sorry.","refusal":"No."}`)))
		if err != nil {
			t.Fatal(err)
		}
		text, finish := textAndFinish(t, evs)
		if text != "Sorry.No." {
			t.Errorf("text = %q, want both parts", text)
		}
		if finish == nil || finish.Reason != llm.FinishContentFilter {
			t.Errorf("finish = %+v, want content_filter", finish)
		}
	})
}

// A refusal the upstream sent in a shape other than a string is no refusal; it must not fail the
// rest of the reply, which decoded before this field was read.
func TestNonStringRefusalDoesNotFailTheReply(t *testing.T) {
	t.Run("delta", func(t *testing.T) {
		evs := streamReply(t, deltaChunk(`{"content":"Hi","refusal":{"x":1}}`), finishChunk)
		text, finish := textAndFinish(t, evs)
		if text != "Hi" {
			t.Errorf("text = %q, want Hi", text)
		}
		if finish == nil || finish.Reason != llm.FinishStop {
			t.Errorf("finish = %+v, want stop", finish)
		}
	})
	t.Run("message", func(t *testing.T) {
		evs, err := DecodeResponse([]byte(completionBody(`{"role":"assistant","content":"Hi","refusal":{"x":1}}`)))
		if err != nil {
			t.Fatal(err)
		}
		text, finish := textAndFinish(t, evs)
		if text != "Hi" {
			t.Errorf("text = %q, want Hi", text)
		}
		if finish == nil || finish.Reason != llm.FinishStop {
			t.Errorf("finish = %+v, want stop", finish)
		}
	})
}
