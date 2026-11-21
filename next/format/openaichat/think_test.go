package openaichat

import "testing"

// The tags are built from pieces so that no tool or editor treats them as markup.
const (
	openTag  = "<" + "think" + ">"
	closeTag = "<" + "/think" + ">"
)

// A reply that opens its content with a think tag sends the tagged run as reasoning and the rest
// as text: some OpenAI-compatible upstreams put their reasoning there.
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
		// The closing tag's prefix is held while it could still be the tag, so the last
		// reasoning delta is the part before it.
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
