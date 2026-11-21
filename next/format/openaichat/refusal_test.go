package openaichat

import (
	"encoding/json"
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

// refusalChunks returns the refusal and non-empty content deltas of a Chat stream, and its finish
// reason.
func refusalChunks(t *testing.T, stream string) (refusal, content []string, finish string) {
	t.Helper()
	for _, block := range strings.Split(stream, "\n\n") {
		data, ok := strings.CutPrefix(block, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content *string `json:"content"`
					Refusal *string `json:"refusal"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("chunk %s: %v", data, err)
		}
		for _, c := range chunk.Choices {
			if c.Delta.Refusal != nil {
				refusal = append(refusal, *c.Delta.Refusal)
			}
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				content = append(content, *c.Delta.Content)
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
		}
	}
	return refusal, content, finish
}

// A Chat client on a Chat upstream gets a refusal in the refusal field, with content left out and
// the upstream's finish reason kept: an OpenAI SDK reads the field, not the content.
func TestRefusalStaysARefusalForChatClient(t *testing.T) {
	t.Run("nonstream", func(t *testing.T) {
		evs, err := DecodeResponse([]byte(completionBody(`{"role":"assistant","content":null,"refusal":"I cannot help with that."}`)))
		if err != nil {
			t.Fatal(err)
		}
		a := NewAggregator()
		for _, ev := range evs {
			a.Add(ev)
		}
		body, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content *string `json:"content"`
					Refusal *string `json:"refusal"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if len(out.Choices) != 1 {
			t.Fatalf("body %s: want one choice", body)
		}
		c := out.Choices[0]
		if c.Message.Content != nil {
			t.Errorf("body %s: content = %q, want null", body, *c.Message.Content)
		}
		if c.Message.Refusal == nil || *c.Message.Refusal != "I cannot help with that." {
			t.Errorf("body %s: refusal = %v, want the refusal text", body, c.Message.Refusal)
		}
		if c.FinishReason != "stop" {
			t.Errorf("body %s: finish_reason = %q, want the upstream's stop", body, c.FinishReason)
		}
	})
	t.Run("stream", func(t *testing.T) {
		evs := streamReply(t, deltaChunk(`{"refusal":"I cannot help with that."}`), finishChunk)
		var out strings.Builder
		w := NewStreamWriter(&out, false)
		for _, ev := range evs {
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		refusal, content, finish := refusalChunks(t, out.String())
		if strings.Join(refusal, "") != "I cannot help with that." {
			t.Errorf("stream %s: refusal %q, want the refusal text", out.String(), refusal)
		}
		if len(content) != 0 {
			t.Errorf("stream %s: content %q, want none", out.String(), content)
		}
		if finish != "stop" {
			t.Errorf("stream %s: finish_reason %q, want stop", out.String(), finish)
		}
	})
}
