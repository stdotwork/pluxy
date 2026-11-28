package openaichat

import "strings"

const (
	thinkOpen  = "<" + "think" + ">"
	thinkClose = "<" + "/think" + ">"
)

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

type thinkState int

const (
	thinkUndecided thinkState = iota
	thinkInside
	thinkOutside
)

type thinkPiece struct {
	text      string
	reasoning bool
}

type thinkFilter struct {
	state   thinkState
	pending string
	inside  string
}

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
		return nil
	}
	switch {
	case trimmed == thinkOpen:
		f.pending = ""
		f.state = thinkInside
		return nil
	case strings.HasPrefix(thinkOpen, trimmed):
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

func partialSuffixLen(s, tag string) int {
	for k := min(len(s), len(tag)-1); k > 0; k-- {
		if strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}
