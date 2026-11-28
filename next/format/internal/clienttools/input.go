package clienttools

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type InputDecoder struct {
	pending []byte
	state   int
	head    int
	closed  bool
	failed  bool
	input   strings.Builder
}

const (
	decHead = iota
	decValue
	decTail
)

var headTokens = [...]string{"{", `"input"`, ":", `"`}

func (d *InputDecoder) Push(fragment string) string {
	if d.failed {
		return ""
	}
	d.pending = append(d.pending, fragment...)
	before := d.input.Len()
	for len(d.pending) > 0 && !d.failed {
		var n int
		switch d.state {
		case decHead:
			n = d.readHead()
		case decValue:
			n = d.readValue()
		default:
			n = d.readTail()
		}
		if n == 0 {
			break
		}
		d.pending = d.pending[n:]
	}
	if d.failed {
		return ""
	}
	return d.input.String()[before:]
}

func (d *InputDecoder) Check(fragment string) {
	d.Push(fragment)
	d.input.Reset()
}

func (d *InputDecoder) Whole() bool {
	return !d.failed && d.state == decTail && d.closed
}

func (d *InputDecoder) fail() int {
	d.failed = true
	return 0
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func (d *InputDecoder) readHead() int {
	p := d.pending
	i := 0
	for i < len(p) && isSpace(p[i]) {
		i++
	}
	tok, rest := headTokens[d.head], string(p[i:])
	if len(rest) < len(tok) {
		if !strings.HasPrefix(tok, rest) {
			return d.fail()
		}
		return i
	}
	if !strings.HasPrefix(rest, tok) {
		return d.fail()
	}
	d.head++
	if d.head == len(headTokens) {
		d.state = decValue
	}
	return i + len(tok)
}

func (d *InputDecoder) readValue() int {
	p := d.pending
	i := 0
	for i < len(p) {
		c := p[i]
		switch {
		case c == '"':
			d.state = decTail
			return i + 1
		case c == '\\':
			r, n, ok := unescape(p[i:])
			if n < 0 {
				return d.fail()
			}
			if !ok {
				return i
			}
			d.input.WriteRune(r)
			i += n
		case c < 0x20:
			return d.fail()
		case c < utf8.RuneSelf:
			d.input.WriteByte(c)
			i++
		default:
			if !utf8.FullRune(p[i:]) {
				return i
			}
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				return d.fail()
			}
			d.input.WriteRune(r)
			i += size
		}
	}
	return i
}

func unescape(p []byte) (r rune, n int, ok bool) {
	if len(p) < 2 {
		return 0, 0, false
	}
	switch p[1] {
	case '"', '\\', '/':
		return rune(p[1]), 2, true
	case 'b':
		return '\b', 2, true
	case 'f':
		return '\f', 2, true
	case 'n':
		return '\n', 2, true
	case 'r':
		return '\r', 2, true
	case 't':
		return '\t', 2, true
	case 'u':
	default:
		return 0, -1, false
	}
	if len(p) < 6 {
		return 0, 0, false
	}
	hi, valid := hex4(p[2:6])
	switch {
	case !valid, hi >= 0xdc00 && hi <= 0xdfff:
		return 0, -1, false
	case hi < 0xd800 || hi > 0xdbff:
		return rune(hi), 6, true
	}
	if len(p) < 12 {
		if !strings.HasPrefix(`\u`, string(p[6:min(len(p), 8)])) {
			return 0, -1, false
		}
		return 0, 0, false
	}
	lo, valid := hex4(p[8:12])
	if p[6] != '\\' || p[7] != 'u' || !valid || lo < 0xdc00 || lo > 0xdfff {
		return 0, -1, false
	}
	return utf16.DecodeRune(rune(hi), rune(lo)), 12, true
}

func hex4(p []byte) (rune, bool) {
	var v rune
	for _, c := range p {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

func (d *InputDecoder) readTail() int {
	for _, c := range d.pending {
		switch {
		case isSpace(c):
		case c == '}' && !d.closed:
			d.closed = true
		default:
			return d.fail()
		}
	}
	return len(d.pending)
}
