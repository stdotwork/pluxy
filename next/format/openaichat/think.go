package openaichat

import "strings"

// Some OpenAI-compatible upstreams put a reply's reasoning in its content, between a leading tag
// and a closing one. The tags are built from pieces here so that nothing in this package reads
// them as markup.
const (
	thinkOpen  = "<" + "think" + ">"
	thinkClose = "<" + "/think" + ">"
)

// parseThink returns the reasoning a reply's content opens with in a think tag, and the text
// after the closing tag. It reads a tag only when it opens the content, after optional leading
// whitespace; a tag later in the text is ordinary text. An unterminated tag makes the rest of the
// content reasoning, as a stream of it whose end arrives before the closing tag does.
func parseThink(content string) (reasoning, text string) {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(trimmed, thinkOpen) {
		return "", content
	}
	rest := trimmed[len(thinkOpen):]
	if i := strings.Index(rest, thinkClose); i >= 0 {
		return rest[:i], rest[i+len(thinkClose):]
	}
	return rest, ""
}

// thinkState is how far the leading-think-tag parse of a reply's content has got.
type thinkState int

const (
	thinkUndecided thinkState = iota // the content so far could still be the opening tag
	thinkInside                      // inside the leading tag
	thinkOutside                     // no leading tag, or past its closing tag: all text
)

// thinkPiece is one run of a reply's content: reasoning inside the tag, or ordinary text.
type thinkPiece struct {
	text      string
	reasoning bool
}

// thinkFilter turns a reply's content into reasoning and text while allowing for a think tag
// that opens it. It holds content only while it could still be the opening tag, or inside the tag
// while it could be the closing one, so ordinary text is never delayed past a possible tag.
type thinkFilter struct {
	state   thinkState
	pending string // in thinkUndecided, the content not yet decided
	inside  string // in thinkInside, reasoning held back as a possible closing tag
}

// feed takes the next piece of content and returns what it settled, in order.
func (f *thinkFilter) feed(s string) []thinkPiece {
	switch f.state {
	case thinkInside:
		return f.feedInside(s)
	case thinkOutside:
		if s == "" {
			return nil
		}
		return []thinkPiece{{text: s}}
	}
	f.pending += s
	trimmed := strings.TrimLeft(f.pending, " \t\r\n")
	if trimmed == "" {
		// Only whitespace so far: a tag may still follow it.
		return nil
	}
	switch {
	case trimmed == thinkOpen:
		f.pending = ""
		f.state = thinkInside
		return nil
	case strings.HasPrefix(thinkOpen, trimmed):
		// Still a possible opening tag, split across chunks.
		return nil
	case strings.HasPrefix(trimmed, thinkOpen):
		rest := trimmed[len(thinkOpen):]
		f.pending = ""
		f.state = thinkInside
		return f.feedInside(rest)
	}
	f.state = thinkOutside
	out := f.pending
	f.pending = ""
	return []thinkPiece{{text: out}}
}

// feedInside takes content once the opening tag is past and returns the reasoning it settles,
// holding back a run that could be the start of the closing tag.
func (f *thinkFilter) feedInside(s string) []thinkPiece {
	f.inside += s
	if i := strings.Index(f.inside, thinkClose); i >= 0 {
		reasoning, rest := f.inside[:i], f.inside[i+len(thinkClose):]
		f.inside = ""
		f.state = thinkOutside
		var out []thinkPiece
		if reasoning != "" {
			out = append(out, thinkPiece{text: reasoning, reasoning: true})
		}
		if rest != "" {
			out = append(out, thinkPiece{text: rest})
		}
		return out
	}
	keep := partialSuffixLen(f.inside, thinkClose)
	if keep >= len(f.inside) {
		return nil
	}
	out := f.inside[:len(f.inside)-keep]
	f.inside = f.inside[len(f.inside)-keep:]
	return []thinkPiece{{text: out, reasoning: true}}
}

// flush returns what the filter still holds when the reply ends: reasoning short of its closing
// tag, or content that never became the opening tag.
func (f *thinkFilter) flush() []thinkPiece {
	switch f.state {
	case thinkInside:
		out := f.inside
		f.inside = ""
		f.state = thinkOutside
		if out == "" {
			return nil
		}
		return []thinkPiece{{text: out, reasoning: true}}
	case thinkUndecided:
		out := f.pending
		f.pending = ""
		f.state = thinkOutside
		if out == "" {
			return nil
		}
		return []thinkPiece{{text: out}}
	}
	return nil
}

// partialSuffixLen returns the length of the longest suffix of s that is a proper prefix of tag,
// which is how much of s could still become the tag.
func partialSuffixLen(s, tag string) int {
	for k := min(len(s), len(tag)-1); k > 0; k-- {
		if strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}
